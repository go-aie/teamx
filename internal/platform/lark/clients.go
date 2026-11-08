package lark

import (
	"sync"
	"time"
)

// ClientFactory shares request queues across clients, including pinned clients.
// A nil or zero factory uses the process-wide production queues. A constructed factory
// owns an isolated set of queues with the supplied pacing; queue ordering,
// cancellation and self-send capacity remain enforced even with zero intervals.
type ClientFactory struct {
	requests *requestBudget
	selfSend *requestBudget
	gates    sync.Map
}

func NewClientFactory(interval, selfSendInterval, cooldown time.Duration) *ClientFactory {
	return &ClientFactory{
		requests: &requestBudget{interval: interval, cooldown: cooldown},
		selfSend: &requestBudget{interval: selfSendInterval, cooldown: cooldown, capacity: 8},
	}
}
