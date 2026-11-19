package room

import (
	"cmp"
	"context"
	"errors"
	"github.com/go-aie/teamx/internal/inputstream"
	"github.com/go-aie/teamx/internal/platform/gitlab"
	"os"
	"slices"
)

func (p *platformInputStorage) sharedConsumption(g inputstream.SharedGrant, inbox inputstream.Inbox, initialize bool) (inputstream.SharedConsumption, error) {
	c, err := inputstream.LoadSharedConsumption(p.store.dir, inputMember(p.id), inbox, g.ID)
	if initialize && errors.Is(err, os.ErrNotExist) {
		c = inputstream.NewSharedConsumption(inbox, g)
		err = inputstream.SaveSharedConsumption(p.store.dir, c, inputPersistence{p.store})
	}
	return c, p.result(err)
}
func (s *inputSession) addSharedCatalog(c *inputstream.Catalog) error {
	p := s.storage.store.sharedHandle.Load()
	if p == nil {
		return nil
	}
	v := p.view.Load()
	g, err := v.grant(inputMember(s.storage.id))
	if err != nil {
		return nil
	}
	consumption, err := s.storage.sharedConsumption(g, v.root.Inbox, false)
	if err != nil {
		return err
	}
	bound, err := s.storage.observeShared(v, g, consumption)
	if err != nil {
		return err
	}
	streams := map[string][]inputstream.StoredRecord{}
	for seq, rec := range v.records {
		physical := v.streams[seq]
		if seq > bound.Through || seq <= max(g.Start, consumption.Through[physical]) || consumption.Skipped[seq] != "" {
			continue
		}
		if _, err := v.entry(s.storage.id, rec.Location); err != nil {
			continue
		}
		streams[physical] = append(streams[physical], rec)
	}
	for physical, records := range streams {
		slices.SortFunc(records, func(a, b inputstream.StoredRecord) int { return cmp.Compare(a.Location.Seq, b.Location.Seq) })
		projection, err := gitlab.InputCapability().Codec.Decode(records[0].Schema, records[0].Payload)
		if err != nil {
			return err
		}
		summary := inputstream.StreamSummary{Ref: inputstream.SharedStreamRef(v.root.Inbox, inputMember(s.storage.id), g.ID, physical), Kind: "shared", Label: projection.Label, SourceLabel: "共享 GitLab 项目", Attention: "normal"}
		if err := c.AddShared(bound, summary, records, "gitlab"); err != nil {
			return err
		}
	}
	return nil
}
func (s *inputSession) sharedStream(ref string) (*sharedInputStorage, inputstream.SharedGrant, string, error) {
	p := s.storage.store.sharedHandle.Load()
	if p == nil {
		return nil, inputstream.SharedGrant{}, "", ErrIdentity
	}
	v := p.view.Load()
	g, err := v.grant(inputMember(s.storage.id))
	if err != nil {
		return p, g, "", err
	}
	for _, physical := range v.streams {
		if inputstream.SharedStreamRef(v.root.Inbox, inputMember(s.storage.id), g.ID, physical) == ref {
			return p, g, physical, nil
		}
	}
	return p, g, "", ErrIdentity
}
func (s *inputSession) reserveShared(ctx context.Context, q inputstream.ReadRequest) (inputstream.ReadAttempt, error) {
	p, g, physical, err := s.sharedStream(q.Stream)
	if err != nil {
		return inputstream.ReadAttempt{}, err
	}
	request, err := inputOwnerValue(ctx, s.storage, "shared_prepare_intent", func(context.Context) (inputstream.SharedPageRequest, error) {
		c, err := s.storage.sharedConsumption(g, p.view.Load().root.Inbox, false)
		if err != nil {
			return inputstream.SharedPageRequest{}, err
		}
		bound, err := s.storage.observeShared(p.view.Load(), g, c)
		if err != nil {
			return inputstream.SharedPageRequest{}, err
		}
		request := inputstream.SharedPageRequest{Bound: &bound.Through, Inbox: p.view.Load().root.Inbox, Partition: p.view.Load().snapshot.Partition, Member: inputMember(s.storage.id), Grant: g.ID, Stream: physical, Execution: s.execution, From: max(g.Start, c.Through[physical])}
		for seq := range c.Skipped {
			if p.view.Load().streams[seq] == physical {
				request.Excluded = append(request.Excluded, seq)
			}
		}
		slices.Sort(request.Excluded)
		return s.storage.repository.ReserveSharedIntent(s.execution, q, request)
	})
	if err != nil {
		return inputstream.ReadAttempt{}, err
	}
	page, err := sharedOwnerValue(ctx, p, func() (inputstream.SharedPage, error) { return p.repository.FixSharedPage(request) })
	if err != nil {
		return inputstream.ReadAttempt{}, err
	}
	return inputOwnerValue(ctx, s.storage, "shared_reserve", func(context.Context) (inputstream.ReadAttempt, error) {
		return s.storage.repository.ReserveShared(s.execution, q, page)
	})
}
func (s *inputSession) confirmShared(ctx context.Context, a inputstream.ReadAttempt) error {
	if a.Shared == nil {
		return ErrIdentity
	}
	p := s.storage.store.sharedHandle.Load()
	if p == nil {
		return ErrIdentity
	}
	page := *a.Shared
	err := s.storage.run(ctx, "shared_confirm", func(context.Context) error {
		c, err := inputstream.LoadSharedConsumption(s.storage.store.dir, inputMember(s.storage.id), page.Request.Inbox, page.Request.Grant)
		if err != nil {
			return err
		}
		if err = c.Confirm(page, a.Proof()); err != nil {
			return err
		}
		if err = inputstream.SaveSharedConsumption(s.storage.store.dir, c, inputPersistence{s.storage.store}); err != nil {
			return s.storage.result(err)
		}
		return s.storage.repository.SettleOwned(s.execution, a.ID, a.Proof())
	})
	if err != nil {
		return err
	}
	_, err = sharedOwnerValue(ctx, p, func() (struct{}, error) { return struct{}{}, p.repository.ReleaseSharedPage(page, a.Proof(), false) })
	return err
}

// A query is local and side-effect free. The member owner installs this count
// in its ordinary lightweight input projection.
func (p *platformInputStorage) sharedPending() (int, error) {
	shared := p.store.sharedHandle.Load()
	if shared == nil {
		return 0, nil
	}
	v := shared.view.Load()
	g, err := v.grant(inputMember(p.id))
	if err != nil {
		state, exists, stateErr := p.repository.CurrentState()
		if stateErr != nil {
			return 0, stateErr
		}
		key := "shared:" + v.root.Inbox.ID
		if exists {
			if old, ok := state.External[key]; ok && old.Pending && v.root.Grants[old.Grant].Status == "closed" {
				old.Pending = false
				if old.Snapshot.Through > 0 {
					old.Done = append(old.Done, inputstream.Location{Partition: old.Snapshot.Partition, Seq: old.Snapshot.Through})
				}
				_, stateErr = p.repository.ObserveExternal(state.Execution, key, old)
				if stateErr != nil {
					return 0, stateErr
				}
			}
		}
		return 0, nil
	}
	c, err := p.sharedConsumption(g, v.root.Inbox, false)
	if err != nil {
		return 0, err
	}
	if _, err := p.observeShared(v, g, c); err != nil {
		return 0, err
	}
	count := 0
	for n, rec := range v.records {
		if n <= max(g.Start, c.Through[v.streams[n]]) || c.Skipped[n] != "" {
			continue
		}
		if _, err := v.entry(p.id, rec.Location); err == nil {
			count++
		}
	}
	return count, nil
}

func (s *service) closeSharedMember(ctx context.Context, id MemberIdentity) error {
	p := s.store.sharedHandle.Load()
	if p == nil {
		return nil
	}
	member, err := s.inputStorage(id)
	if err != nil {
		return err
	}
	member.historyHandoff.Lock()
	defer member.historyHandoff.Unlock()
	for _, g := range p.view.Load().root.Grants {
		if g.Member != inputMember(id) || g.Status == "closed" {
			continue
		}
		if _, err := sharedOwnerValue(ctx, p, func() (struct{}, error) { return struct{}{}, p.repository.CloseSharedGrant(g.ID) }); err != nil {
			return err
		}
		member, err := s.inputStorage(id)
		if err != nil {
			return err
		}
		if err := member.run(ctx, "shared_grant_change", func(context.Context) error { member.catalogs = nil; return nil }); err != nil {
			return err
		}
		for _, page := range p.view.Load().root.Pages {
			if page.Request.Grant != g.ID || page.State != "protected" {
				continue
			}

			err = member.run(ctx, "shared_revoke", func(context.Context) error {
				state, exists, err := member.repository.CurrentState()
				if err != nil {
					return err
				}
				if !exists {
					return ErrIdentity
				}
				for _, a := range state.Attempts {
					if a.ID == page.Request.Page {
						if a.Phase == "awaiting_confirmation" {
							return ErrBusy
						}
						if a.Phase != "failed_before_output" && a.Phase != "settled" {
							if err := member.repository.Abort(state.Execution, a.ID, "shared_grant_revoked"); err != nil {
								return err
							}
						}
					}
				}
				return member.repository.AbortSharedIntent(state.Execution, page.Request.Page)
			})
			if err != nil {
				return err
			}
			if current, ok := p.view.Load().root.Pages[page.Request.Page]; ok && current.State != "protected" {
				continue
			}
			proof := inputstream.Ref("shared_abort_", page.Digest, "shared_grant_revoked")
			if _, err := sharedOwnerValue(ctx, p, func() (struct{}, error) { return struct{}{}, p.repository.ReleaseSharedPage(page, proof, true) }); err != nil {
				return err
			}
		}
		if _, err := sharedOwnerValue(ctx, p, func() (struct{}, error) { return struct{}{}, p.repository.CloseSharedGrant(g.ID) }); err != nil {
			return err
		}
	}
	s.sharedInputsChanged(p)
	return nil
}

// Called inside the member owner. It freezes an offer once per execution and
// reports only member progress inside that range to the existing recovery gate.
func (p *platformInputStorage) observeShared(v *sharedInputView, g inputstream.SharedGrant, c inputstream.SharedConsumption) (inputstream.Snapshot, error) {
	state, exists, err := p.repository.CurrentState()
	if err != nil {
		return inputstream.Snapshot{}, err
	}
	if !exists {
		return v.snapshot, nil
	}
	bound := v.snapshot
	key := "shared:" + v.root.Inbox.ID
	if _, offered := state.External[key]; !offered {
		available := false
		for seq, rec := range v.records {
			if seq > g.Start && slices.Contains(rec.Recipients, g.ID) {
				available = true
				break
			}
		}
		if !available {
			return bound, nil
		}
	}
	if old, ok := state.External[key]; ok {
		if old.Grant != g.ID {
			return inputstream.Snapshot{Partition: v.snapshot.Partition, Through: g.Start}, nil
		}
		bound = old.Snapshot
	}
	o := inputstream.ExternalObservation{Snapshot: bound, Grant: g.ID, Done: []inputstream.Location{}}
	if v.snapshot.Reclaimed > bound.Reclaimed {
		o.Done = append(o.Done, inputstream.Location{Partition: bound.Partition, Seq: min(v.snapshot.Reclaimed, bound.Through)})
	}
	for seq, rec := range v.records {
		if seq > bound.Through || seq <= g.Start || !slices.Contains(rec.Recipients, g.ID) {
			continue
		}
		if c.Through[v.streams[seq]] >= seq || c.Skipped[seq] != "" {
			o.Done = append(o.Done, rec.Location)
		} else {
			o.Pending = true
		}
	}
	inputstream.SortedLocations(o.Done)
	_, err = p.repository.ObserveExternal(state.Execution, key, o)
	return bound, err
}
