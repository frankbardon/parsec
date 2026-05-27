package channels

import (
	"context"
	"testing"
	"time"
)

func TestRedisEventBus_RemoteFanout(t *testing.T) {
	clientA := newTestRedisClient(t)
	defer clientA.Close()

	busA := NewRedisEventBus(clientA, "").WithKeyPrefix("parsec-bus-" + t.Name())
	busB := NewRedisEventBus(clientA, "").WithKeyPrefix("parsec-bus-" + t.Name())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	got := make(chan Event, 4)
	ready := make(chan struct{})
	go func() {
		close(ready)
		_ = busB.Run(ctx, func(ev Event) { got <- ev })
	}()
	<-ready
	// Give the subscription a moment to land.
	time.Sleep(50 * time.Millisecond)

	n, _ := ParseName("public:test.system.status")
	if err := busA.Publish(ctx, Event{Kind: EventOpened, Name: n, At: time.Now()}); err != nil {
		t.Fatal(err)
	}
	select {
	case ev := <-got:
		if ev.Kind != EventOpened || ev.Name.String() != n.String() {
			t.Fatalf("unexpected event: %+v", ev)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for cross-bus event")
	}
}

func TestRedisEventBus_FiltersOwnEcho(t *testing.T) {
	client := newTestRedisClient(t)
	defer client.Close()

	bus := NewRedisEventBus(client, "node-self").WithKeyPrefix("parsec-bus-self-" + t.Name())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	got := make(chan Event, 4)
	ready := make(chan struct{})
	go func() {
		close(ready)
		_ = bus.Run(ctx, func(ev Event) { got <- ev })
	}()
	<-ready
	time.Sleep(50 * time.Millisecond)

	n, _ := ParseName("public:test.system.status")
	if err := bus.Publish(ctx, Event{Kind: EventOpened, Name: n, At: time.Now()}); err != nil {
		t.Fatal(err)
	}
	select {
	case ev := <-got:
		t.Fatalf("expected own publish filtered; got %+v", ev)
	case <-time.After(300 * time.Millisecond):
		// good — no echo
	}
}

func TestRedisEventBus_ManagerCrossNode(t *testing.T) {
	clientA := newTestRedisClient(t)
	prefix := "parsec-mgr-" + t.Name()
	storeA := NewRedisStore(clientA).WithKeyPrefix(prefix)
	storeB := NewRedisStore(clientA).WithKeyPrefix(prefix)
	// Pre-emptive cleanup in case a prior run leaked state.
	{
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		clientA.Del(ctx, storeA.hashKey())
		cancel()
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		clientA.Del(ctx, storeA.hashKey())
		_ = clientA.Close()
	})

	mgrA := NewManagerWithStore(storeA)
	mgrB := NewManagerWithStore(storeB)

	busA := NewRedisEventBus(clientA, "node-a").WithKeyPrefix(prefix)
	busB := NewRedisEventBus(clientA, "node-b").WithKeyPrefix(prefix)
	mgrA.SetEventBus(busA)
	mgrB.SetEventBus(busB)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	gotB := make(chan Event, 8)
	mgrB.Subscribe(gotB)
	go mgrA.RunEventBus(ctx)
	go mgrB.RunEventBus(ctx)
	time.Sleep(100 * time.Millisecond) // let subscriptions land

	n, _ := ParseName("public:cross.system.status")
	if _, err := mgrA.OpenPublic(n, time.Minute); err != nil {
		t.Fatal(err)
	}
	// mgrB should see the EventOpened relayed via bus.
	select {
	case ev := <-gotB:
		if ev.Kind != EventOpened {
			t.Fatalf("expected opened, got %s", ev.Kind)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout: node B did not see node A's lifecycle event")
	}

	// Delete on B; A should observe it too.
	gotA := make(chan Event, 8)
	mgrA.Subscribe(gotA)
	if err := mgrB.Delete(n); err != nil {
		t.Fatal(err)
	}
	select {
	case ev := <-gotA:
		if ev.Kind != EventDeleted {
			t.Fatalf("expected deleted on A, got %s", ev.Kind)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout: node A did not see node B's delete event")
	}
}
