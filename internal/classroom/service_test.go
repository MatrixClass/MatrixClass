package classroom

import (
	"fmt"
	"nexlab/internal/core"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestRosterAndTelemetry(t *testing.T) {
	svc := NewService()

	var notifyCount atomic.Int32
	unsubscribe := svc.OnChange(func() {
		notifyCount.Add(1)
	})

	svc.UpsertStudent(core.StudentInfo{
		ID:        "s3",
		Name:      "Charlie",
		StudentNo: "2026003",
		Seat:      core.SeatPos{Row: 1, Col: 0},
	})
	svc.UpsertStudent(core.StudentInfo{
		ID:        "s1",
		Name:      "Bob",
		StudentNo: "2026002",
		Seat:      core.SeatPos{Row: 0, Col: 1},
	})
	svc.UpsertStudent(core.StudentInfo{
		ID:        "s2",
		Name:      "Alice",
		StudentNo: "2026001",
		Seat:      core.SeatPos{Row: 0, Col: 1},
	})

	list := svc.ListStudents()
	if len(list) != 3 {
		t.Fatalf("expected 3 students, got %d", len(list))
	}
	// Row 0 Col 1: Alice before Bob by name, then Row 1 Col 0: Charlie.
	if list[0].ID != "s2" || list[1].ID != "s1" || list[2].ID != "s3" {
		t.Fatalf("unexpected roster sort order: %+v", list)
	}

	if err := svc.UpdateTelemetry("s1", 12, 0.02); err != nil {
		t.Fatalf("UpdateTelemetry failed: %v", err)
	}
	st, ok := svc.GetStudent("s1")
	if !ok || st.RTTMs != 12 || st.LossRate != 0.02 {
		t.Fatalf("unexpected student telemetry: %+v", st)
	}

	if err := svc.SetStudentStatus("s1", core.StatusLocked); err != nil {
		t.Fatalf("SetStudentStatus failed: %v", err)
	}
	st, _ = svc.GetStudent("s1")
	if st.Status != core.StatusLocked {
		t.Fatalf("expected locked status, got %s", st.Status)
	}

	// Stale offline detection.
	stale := core.StudentInfo{
		ID:       "s4",
		Name:     "Dave",
		Status:   core.StatusOnline,
		LastSeen: time.Now().Add(-5 * time.Minute),
	}
	svc.UpsertStudent(stale)
	marked := svc.MarkOfflineStale(1 * time.Minute)
	if len(marked) != 1 || marked[0] != "s4" {
		t.Fatalf("expected [s4] marked offline, got %v", marked)
	}
	st4, _ := svc.GetStudent("s4")
	if st4.Status != core.StatusOffline {
		t.Fatalf("expected s4 offline, got %s", st4.Status)
	}

	beforeUnsub := notifyCount.Load()
	unsubscribe()
	svc.RemoveStudent("s4")
	if notifyCount.Load() != beforeUnsub {
		t.Fatalf("expected no notification after unsubscribe")
	}
}

func TestGroupsAndLeaders(t *testing.T) {
	svc := NewService()
	svc.UpsertStudent(core.StudentInfo{ID: "s1", Name: "Alice"})
	svc.UpsertStudent(core.StudentInfo{ID: "s2", Name: "Bob"})
	svc.UpsertStudent(core.StudentInfo{ID: "s3", Name: "Charlie"})

	svc.SetGroups([]core.GroupInfo{
		{ID: "g1", Name: "Alpha", Members: []string{"s1", "s2"}},
		{ID: "g2", Name: "Beta"},
	})

	g1, ok := svc.GetGroup("g1")
	if !ok || g1.LeaderID != "s1" {
		t.Fatalf("expected default leader s1 for g1, got %+v", g1)
	}

	if err := svc.SetGroupLeader("g1", "s2"); err != nil {
		t.Fatalf("SetGroupLeader failed: %v", err)
	}
	if err := svc.SetGroupLeader("g1", "s3"); err != ErrInvalidLeader {
		t.Fatalf("expected ErrInvalidLeader, got %v", err)
	}

	if err := svc.AddGroupMember("g2", "s2"); err != nil {
		t.Fatalf("AddGroupMember failed: %v", err)
	}
	st2, _ := svc.GetStudent("s2")
	if st2.GroupID != "g2" {
		t.Fatalf("expected s2 moved to g2, got %q", st2.GroupID)
	}
	g1, _ = svc.GetGroup("g1")
	if len(g1.Members) != 1 || g1.Members[0] != "s1" || g1.LeaderID != "s1" {
		t.Fatalf("expected g1 updated after moving s2, got %+v", g1)
	}

	if err := svc.RemoveGroupMember("g2", "s2"); err != nil {
		t.Fatalf("RemoveGroupMember failed: %v", err)
	}
	st2, _ = svc.GetStudent("s2")
	if st2.GroupID != "" {
		t.Fatalf("expected s2 GroupID cleared, got %q", st2.GroupID)
	}

	svc.RemoveStudent("s1")
	g1, _ = svc.GetGroup("g1")
	if len(g1.Members) != 0 || g1.LeaderID != "" {
		t.Fatalf("expected g1 empty after removing s1, got %+v", g1)
	}
}

func TestSeatingGrid(t *testing.T) {
	svc := NewService()
	svc.UpsertStudent(core.StudentInfo{ID: "s1", Name: "Alice", StudentNo: "01"})
	svc.UpsertStudent(core.StudentInfo{ID: "s2", Name: "Bob", StudentNo: "02"})
	svc.UpsertStudent(core.StudentInfo{ID: "s3", Name: "Charlie", StudentNo: "03"})
	svc.UpsertStudent(core.StudentInfo{ID: "s4", Name: "Dave", StudentNo: "04"})
	svc.UpsertStudent(core.StudentInfo{ID: "s5", Name: "Eve", StudentNo: "05"})

	svc.AutoLayoutSeats(2, 3)
	st4, _ := svc.GetStudent("s4")
	st5, _ := svc.GetStudent("s5")
	if st4.Seat != (core.SeatPos{Row: 1, Col: 0}) || st5.Seat != (core.SeatPos{Row: 1, Col: 1}) {
		t.Fatalf("unexpected auto layout seats: s4=%+v s5=%+v", st4.Seat, st5.Seat)
	}

	svc.UpdateSeat("s1", core.SeatPos{Row: 2, Col: 2})
	st1, _ := svc.GetStudent("s1")
	if st1.Seat != (core.SeatPos{Row: 2, Col: 2}) {
		t.Fatalf("unexpected updated seat: %+v", st1.Seat)
	}

	if err := svc.SwapSeats("s1", "s4"); err != nil {
		t.Fatalf("SwapSeats failed: %v", err)
	}
	st1, _ = svc.GetStudent("s1")
	st4, _ = svc.GetStudent("s4")
	if st1.Seat != (core.SeatPos{Row: 1, Col: 0}) || st4.Seat != (core.SeatPos{Row: 2, Col: 2}) {
		t.Fatalf("unexpected swapped seats: s1=%+v s4=%+v", st1.Seat, st4.Seat)
	}
}

func TestCheckInHandQueueAndChat(t *testing.T) {
	svc := NewService()
	svc.UpsertStudent(core.StudentInfo{ID: "s1", Name: "Alice", CheckedIn: true})
	svc.UpsertStudent(core.StudentInfo{ID: "s2", Name: "Bob", CheckedIn: true})

	svc.StartCheckIn()
	summary := svc.CheckInSummary()
	if !summary.Active || summary.CheckedIn != 0 || summary.Pending != 2 {
		t.Fatalf("expected reset check-in summary, got %+v", summary)
	}

	svc.SubmitCheckIn("s2", "Bob Updated", "2026099")
	summary = svc.CheckInSummary()
	if summary.CheckedIn != 1 || summary.Pending != 1 {
		t.Fatalf("expected 1 checked in and 1 pending, got %+v", summary)
	}
	st2, _ := svc.GetStudent("s2")
	if st2.Name != "Bob Updated" || st2.StudentNo != "2026099" || !st2.CheckedIn {
		t.Fatalf("unexpected student after check-in: %+v", st2)
	}
	svc.EndCheckIn()
	if svc.CheckInSummary().Active {
		t.Fatalf("expected check-in inactive after EndCheckIn")
	}

	// Hand raise FIFO queue.
	svc.RaiseHand("s2", true)
	svc.RaiseHand("s1", true)
	svc.RaiseHand("s2", true) // duplicate should keep original position
	raised := svc.ListRaisedHands()
	if len(raised) != 2 || raised[0].ID != "s2" || raised[1].ID != "s1" {
		t.Fatalf("expected FIFO hand queue [s2, s1], got %+v", raised)
	}

	svc.RaiseHand("s2", false)
	raised = svc.ListRaisedHands()
	if len(raised) != 1 || raised[0].ID != "s1" {
		t.Fatalf("expected [s1] after lowering s2, got %+v", raised)
	}

	svc.ClearHands()
	if len(svc.ListRaisedHands()) != 0 {
		t.Fatalf("expected empty hand queue after ClearHands")
	}

	// Chat and announcement.
	svc.AddChat(core.ChatMessage{SenderID: "s1", Sender: "Alice", TargetID: "teacher", Text: "Hi"})
	ann := svc.BroadcastAnnouncement("t1", "Teacher", "Class starts now")
	chats := svc.ListChats()
	if len(chats) != 2 || chats[1].ID != ann.ID || chats[1].TargetID != "all" {
		t.Fatalf("unexpected chats: %+v", chats)
	}
}

func TestConcurrentAccess(t *testing.T) {
	svc := NewService()
	var wg sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			id := fmt.Sprintf("s-%d", w)
			svc.UpsertStudent(core.StudentInfo{ID: id, Name: id})
			svc.UpdateSeat(id, core.SeatPos{Row: w / 4, Col: w % 4})
			svc.RaiseHand(id, true)
			svc.SubmitCheckIn(id, id, id)
			svc.AddChat(core.ChatMessage{SenderID: id, Sender: id, Text: "ping"})
			_ = svc.ListStudents()
			_ = svc.ListRaisedHands()
			_ = svc.ListChats()
		}(worker)
	}
	wg.Wait()
	if len(svc.ListStudents()) != 8 {
		t.Fatalf("expected 8 students, got %d", len(svc.ListStudents()))
	}
}
