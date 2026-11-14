package events

import (
	"sync"
	"testing"
)

// ---- EventBus 增强测试：Until / Once / 通配 / 优先级（events.go 新增 API）----

// record 生成一个向 order 追加标记的监听器（测试辅助）。
func record(order *[]string, tag string) Listener {
	return func(e Event) { *order = append(*order, tag) }
}

func TestEventBusPriorityOrder(t *testing.T) {
	tests := []struct {
		name      string
		register  func(b *EventBus, order *[]string)
		wantOrder []string
	}{
		{
			"数字小先执行，同优先级按注册序",
			func(b *EventBus, order *[]string) {
				b.ListenWithPriority("order.p", record(order, "p5"), 5)
				b.ListenWithPriority("order.p", record(order, "p1-first"), 1)
				b.ListenWithPriority("order.p", record(order, "p1-second"), 1)
				b.ListenWithPriority("order.p", record(order, "p0"), 0)
			},
			[]string{"p0", "p1-first", "p1-second", "p5"},
		},
		{
			"Listen 等价于优先级 0 并按注册序稳定",
			func(b *EventBus, order *[]string) {
				b.Listen("order.plain", record(order, "a"))
				b.ListenWithPriority("order.plain", record(order, "hi"), 9)
				b.Listen("order.plain", record(order, "b"))
			},
			[]string{"a", "b", "hi"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := NewEventBus()
			order := []string{}
			tt.register(b, &order)
			b.Dispatch(ModelEvent{Name: "order.p", Model: nil})
			b.Dispatch(ModelEvent{Name: "order.plain", Model: nil})
			// 两个用例各自只命中自己的事件名，order 汇总后核对前缀
			if len(order) != len(tt.wantOrder) {
				t.Fatalf("order = %v; want %v", order, tt.wantOrder)
			}
			for i, tag := range tt.wantOrder {
				if order[i] != tag {
					t.Fatalf("order = %v; want %v", order, tt.wantOrder)
				}
			}
		})
	}
}

func TestEventBusWildcard(t *testing.T) {
	tests := []struct {
		name      string
		eventName string
		register  func(b *EventBus, order *[]string)
		wantOrder []string
	}{
		{
			"精确优先，其后最长前缀通配",
			"user.created",
			func(b *EventBus, order *[]string) {
				b.Listen("user.*", record(order, "wild-short"))
				b.Listen("user.creat*", record(order, "wild-long"))
				b.Listen("user.created", record(order, "exact"))
			},
			[]string{"exact", "wild-long", "wild-short"},
		},
		{
			"仅通配命中",
			"user.deleted",
			func(b *EventBus, order *[]string) {
				b.Listen("user.created", record(order, "exact-other"))
				b.Listen("user.*", record(order, "wild"))
			},
			[]string{"wild"},
		},
		{
			"通配不命中其他前缀",
			"post.created",
			func(b *EventBus, order *[]string) {
				b.Listen("user.*", record(order, "wild-user"))
			},
			[]string{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := NewEventBus()
			order := []string{}
			tt.register(b, &order)
			b.Dispatch(ModelEvent{Name: tt.eventName, Model: nil})
			if len(order) != len(tt.wantOrder) {
				t.Fatalf("order = %v; want %v", order, tt.wantOrder)
			}
			for i, tag := range tt.wantOrder {
				if order[i] != tag {
					t.Fatalf("order = %v; want %v", order, tt.wantOrder)
				}
			}
		})
	}
}

func TestEventBusDispatchUntil(t *testing.T) {
	t.Run("返回 false 停止传播", func(t *testing.T) {
		b := NewEventBus()
		order := []string{}
		b.ListenUntil("until.halt", func(e Event) bool {
			order = append(order, "until-1")
			return true
		})
		b.ListenUntil("until.halt", func(e Event) bool {
			order = append(order, "until-2")
			return false // 中断
		})
		b.Listen("until.halt", record(&order, "after"))
		got := b.DispatchUntil(ModelEvent{Name: "until.halt", Model: nil})
		if got {
			t.Error("DispatchUntil 应返回 false")
		}
		if len(order) != 2 || order[0] != "until-1" || order[1] != "until-2" {
			t.Errorf("order = %v; 中断后的监听器不应执行", order)
		}
	})

	t.Run("全部通过返回 true，普通监听器继续传播", func(t *testing.T) {
		b := NewEventBus()
		order := []string{}
		b.Listen("until.pass", record(&order, "plain"))
		b.ListenUntil("until.pass", func(e Event) bool {
			order = append(order, "until")
			return true
		})
		got := b.DispatchUntil(ModelEvent{Name: "until.pass", Model: nil})
		if !got {
			t.Error("DispatchUntil 应返回 true")
		}
		if len(order) != 2 || order[0] != "plain" || order[1] != "until" {
			t.Errorf("order = %v", order)
		}
	})

	t.Run("带优先级的 until 监听", func(t *testing.T) {
		b := NewEventBus()
		order := []string{}
		b.ListenUntilWithPriority("until.prio", func(e Event) bool {
			order = append(order, "late")
			return true
		}, 5)
		b.ListenUntilWithPriority("until.prio", func(e Event) bool {
			order = append(order, "early-halt")
			return false
		}, 1)
		got := b.DispatchUntil(ModelEvent{Name: "until.prio", Model: nil})
		if got || len(order) != 1 || order[0] != "early-halt" {
			t.Errorf("got=%v order=%v", got, order)
		}
	})
}

func TestEventBusOnce(t *testing.T) {
	tests := []struct {
		name       string
		register   func(b *EventBus, counter *int, order *[]string)
		dispatches int
		wantFired  int
		wantOthers int
	}{
		{
			"一次性监听器只触发一次",
			func(b *EventBus, counter *int, order *[]string) {
				b.Once("once.a", func(e Event) { *counter++ })
				b.Listen("once.a", func(e Event) { *counter += 100 })
			},
			3, 1, 300,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := NewEventBus()
			counter := 0
			order := []string{}
			tt.register(b, &counter, &order)
			for i := 0; i < tt.dispatches; i++ {
				b.Dispatch(ModelEvent{Name: "once.a", Model: nil})
			}
			// once 贡献 1，普通监听器每次 +100
			if counter != tt.wantFired+tt.wantOthers {
				t.Errorf("counter = %d; want %d", counter, tt.wantFired+tt.wantOthers)
			}
		})
	}

	t.Run("并发分发下 once 仅执行一次", func(t *testing.T) {
		b := NewEventBus()
		var mu sync.Mutex
		fired := 0
		b.Once("once.race", func(e Event) {
			mu.Lock()
			fired++
			mu.Unlock()
		})
		var wg sync.WaitGroup
		for g := 0; g < 8; g++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for i := 0; i < 50; i++ {
					b.Dispatch(ModelEvent{Name: "once.race", Model: nil})
				}
			}()
		}
		wg.Wait()
		if fired != 1 {
			t.Errorf("once fired = %d; want 1", fired)
		}
	})
}

func TestEventBusBackwardCompatibleDispatch(t *testing.T) {
	// 既有 API（Listen + Dispatch）在无通配/优先级时行为保持：按注册顺序同步执行。
	b := NewEventBus()
	order := []string{}
	b.Listen("legacy.e", record(&order, "first"))
	b.Listen("legacy.e", record(&order, "second"))
	var d Dispatcher = b // 仍满足 Dispatcher 接口
	d.Dispatch(ModelEvent{Name: "legacy.e", Model: nil})
	if len(order) != 2 || order[0] != "first" || order[1] != "second" {
		t.Errorf("order = %v", order)
	}
}
