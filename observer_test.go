package torm

import (
	"database/sql/driver"
	"errors"
	"testing"
	"time"
)

func TestObserverLifecycleOrder(t *testing.T) {
	conn, st := newFakeDB(t)
	st.On("BEGIN", &fakeResult{})
	st.On("COMMIT", &fakeResult{})
	st.On("insert into `posts` (`created_at`, `title`, `updated_at`) values (?, ?, ?)", &fakeResult{Affected: 1, LastID: 5})

	var events []string
	ob := &Observer{
		Saving:   func(any) error { events = append(events, "saving"); return nil },
		Creating: func(any) error { events = append(events, "creating"); return nil },
		Created:  func(any) error { events = append(events, "created"); return nil },
		Saved:    func(any) error { events = append(events, "saved"); return nil },
	}
	p := Post{}
	conn.Observe(&p, ob)

	if err := conn.Create(&p); err != nil {
		t.Fatal(err)
	}
	want := []string{"saving", "creating", "created", "saved"}
	if len(events) != 4 || events[0] != want[0] || events[1] != want[1] || events[2] != want[2] || events[3] != want[3] {
		t.Errorf("events = %v, want %v", events, want)
	}
}

func TestObserverCreatingAbort(t *testing.T) {
	conn, st := newFakeDB(t)
	wantErr := errors.New("abort creating")
	p := Post{}
	conn.Observe(&p, &Observer{Creating: func(any) error { return wantErr }})

	err := conn.Create(&p)
	if !errors.Is(err, wantErr) {
		t.Fatalf("err = %v", err)
	}
	// SQL 不应执行
	for _, call := range st.Calls() {
		if len(call.Sql) >= 6 && call.Sql[:6] == "insert" {
			t.Error("SQL must not run when creating aborts")
		}
	}
}

func TestObserverUpdateAndDelete(t *testing.T) {
	conn, st := newFakeDB(t)
	now := time.Now()
	p := Post{ID: 2, Title: "t", CreatedAt: now, UpdatedAt: now}
	updateSQL := "update `posts` set `created_at` = ?, `title` = ?, `updated_at` = ? where `id` = ?"
	st.On("BEGIN", &fakeResult{})
	st.On(updateSQL, &fakeResult{Affected: 1})
	deleteSQL := "delete from `posts` where `id` = ?"
	st.On(deleteSQL, &fakeResult{Affected: 1})

	var events []string
	conn.Observe(&p, &Observer{
		Updating: func(any) error { events = append(events, "updating"); return nil },
		Updated:  func(any) error { events = append(events, "updated"); return nil },
		Saving:   func(any) error { events = append(events, "saving"); return nil },
		Saved:    func(any) error { events = append(events, "saved"); return nil },
		Deleting: func(any) error { events = append(events, "deleting"); return nil },
		Deleted:  func(any) error { events = append(events, "deleted"); return nil },
	})

	if err := conn.Save(&p); err != nil {
		t.Fatal(err)
	}
	if len(events) != 4 || events[0] != "saving" || events[1] != "updating" || events[2] != "updated" || events[3] != "saved" {
		t.Errorf("update events = %v", events)
	}

	events = nil
	if err := conn.ForceDelete(&p); err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[0] != "deleting" || events[1] != "deleted" {
		t.Errorf("delete events = %v", events)
	}
	st.LastCall(t, deleteSQL)
}

func TestObserverRetrieved(t *testing.T) {
	conn, st := newFakeDB(t)
	st.On("select * from `users`", &fakeResult{
		Columns: []string{"id", "name"},
		Rows:   [][]driver.Value{{int64(1), "A"}, {int64(2), "B"}},
	})

	count := 0
	var u User
	conn.Observe(&u, &Observer{Retrieved: func(any) { count++ }})

	var users []User
	if err := conn.Table("users").Get(&users); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Errorf("retrieved = %d, want 2", count)
	}
}

func TestEventDispatcher(t *testing.T) {
	conn, st := newFakeDB(t)
	bus := NewEventBus()
	conn.SetEventDispatcher(bus)

	var names []string
	for _, name := range []string{EventSaving, EventCreating, EventCreated, EventSaved} {
		bus.Listen(name, func(e Event) { names = append(names, e.EventName()) })
	}

	p := Post{}
	st.On("BEGIN", &fakeResult{})
	st.On("insert into `posts` (`created_at`, `title`, `updated_at`) values (?, ?, ?)", &fakeResult{Affected: 1, LastID: 1})

	if err := conn.Create(&p); err != nil {
		t.Fatal(err)
	}
	want := []string{"saving", "creating", "created", "saved"}
	if len(names) != 4 {
		t.Fatalf("events = %v, want %v", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Errorf("events = %v, want %v", names, want)
			break
		}
	}
}

func TestEventDispatcherRetrieved(t *testing.T) {
	conn, st := newFakeDB(t)
	bus := NewEventBus()
	conn.SetEventDispatcher(bus)
	st.On("select * from `users`", &fakeResult{
		Columns: []string{"id", "name"},
		Rows:   [][]driver.Value{{int64(1), "A"}},
	})

	count := 0
	bus.Listen(EventRetrieved, func(e Event) { count++ })
	var users []User
	if err := conn.Table("users").Get(&users); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("retrieved events = %d", count)
	}
}
