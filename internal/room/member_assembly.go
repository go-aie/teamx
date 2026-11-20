package room

import (
	"context"

	"github.com/go-aie/actorx"
)

type bindTurnScheduler struct{ Scheduler, Target actorx.Addr }

// Spawn first, then bind the reply address before attaching/enabling the member.
// Test observers may use the same assembly without becoming scheduled agents.
func spawnScheduledMember(rt *actorx.Runtime, member actorx.Actor, scheduler actorx.Addr) (actorx.Addr, error) {
	addr, err := rt.Spawn(member)
	if err != nil {
		return addr, err
	}
	if _, ok := member.(*MemberAgent); ok {
		_, err = addr.Call[struct{}](context.Background(), bindTurnScheduler{Scheduler: scheduler, Target: addr})
		if err != nil {
			_ = addr.Stop(err)
			_, _ = addr.Wait(context.Background())
		}
	}
	return addr, err
}
