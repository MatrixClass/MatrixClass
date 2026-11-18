package classroom

import (
	"errors"
	"fmt"
	"math"
	"nexlab/internal/core"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

var (
	ErrStudentNotFound = errors.New("classroom: student not found")
	ErrGroupNotFound   = errors.New("classroom: group not found")
	ErrInvalidLeader   = errors.New("classroom: leader must be a member of the group")
)

type CheckInSummary struct {
	Active     bool      `json:"active"`
	StartedAt  time.Time `json:"started_at"`
	Total      int       `json:"total"`
	CheckedIn  int       `json:"checked_in"`
	Pending    int       `json:"pending"`
	CheckedIDs []string  `json:"checked_ids"`
	PendingIDs []string  `json:"pending_ids"`
}

type listenerEntry struct {
	id uint64
	fn func()
}

type Service struct {
	mu               sync.RWMutex
	students         map[string]core.StudentInfo
	groups           map[string]core.GroupInfo
	handQueue        []string
	chats            []core.ChatMessage
	checkInActive    bool
	checkInStartedAt time.Time
	listeners        []listenerEntry
	nextListenerID   atomic.Uint64
	nextChatID       atomic.Uint64
}

var _ core.ClassroomService = (*Service)(nil)

func NewService() *Service {
	return &Service{
		students: make(map[string]core.StudentInfo),
		groups:   make(map[string]core.GroupInfo),
	}
}

func (s *Service) OnChange(fn func()) func() {
	if fn == nil {
		return func() {}
	}
	id := s.nextListenerID.Add(1)
	s.mu.Lock()
	s.listeners = append(s.listeners, listenerEntry{id: id, fn: fn})
	s.mu.Unlock()

	return func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		for idx, entry := range s.listeners {
			if entry.id == id {
				s.listeners = append(s.listeners[:idx], s.listeners[idx+1:]...)
				break
			}
		}
	}
}

func (s *Service) snapshotListenersLocked() []func() {
	if len(s.listeners) == 0 {
		return nil
	}
	callbacks := make([]func(), len(s.listeners))
	for idx, entry := range s.listeners {
		callbacks[idx] = entry.fn
	}
	return callbacks
}

func notifyListeners(callbacks []func()) {
	for _, fn := range callbacks {
		fn()
	}
}

func (s *Service) UpsertStudent(info core.StudentInfo) {
	if info.ID == "" {
		return
	}
	if info.Status == "" {
		info.Status = core.StatusOnline
	}
	if info.LastSeen.IsZero() {
		info.LastSeen = time.Now()
	}

	s.mu.Lock()
	s.students[info.ID] = info
	s.syncHandQueueLocked(info.ID, info.HandRaised)
	callbacks := s.snapshotListenersLocked()
	s.mu.Unlock()

	notifyListeners(callbacks)
}

func (s *Service) RemoveStudent(id string) {
	s.mu.Lock()
	if _, ok := s.students[id]; !ok {
		s.mu.Unlock()
		return
	}
	delete(s.students, id)
	s.syncHandQueueLocked(id, false)
	for gid, grp := range s.groups {
		changed := false
		filtered := make([]string, 0, len(grp.Members))
		for _, memberID := range grp.Members {
			if memberID == id {
				changed = true
				continue
			}
			filtered = append(filtered, memberID)
		}
		if changed {
			grp.Members = filtered
			if grp.LeaderID == id {
				if len(filtered) > 0 {
					grp.LeaderID = filtered[0]
				} else {
					grp.LeaderID = ""
				}
			}
			s.groups[gid] = grp
		}
	}
	callbacks := s.snapshotListenersLocked()
	s.mu.Unlock()

	notifyListeners(callbacks)
}

func (s *Service) GetStudent(id string) (core.StudentInfo, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	st, ok := s.students[id]
	return st, ok
}

func (s *Service) SetStudentStatus(id string, status core.StudentStatus) error {
	s.mu.Lock()
	st, ok := s.students[id]
	if !ok {
		s.mu.Unlock()
		return ErrStudentNotFound
	}
	st.Status = status
	if status != core.StatusOffline {
		st.LastSeen = time.Now()
	}
	if status == core.StatusHandRaised {
		st.HandRaised = true
		s.syncHandQueueLocked(id, true)
	}
	s.students[id] = st
	callbacks := s.snapshotListenersLocked()
	s.mu.Unlock()

	notifyListeners(callbacks)
	return nil
}

func (s *Service) UpdateTelemetry(id string, rttMs int, lossRate float64) error {
	s.mu.Lock()
	st, ok := s.students[id]
	if !ok {
		s.mu.Unlock()
		return ErrStudentNotFound
	}
	st.RTTMs = rttMs
	st.LossRate = lossRate
	st.LastSeen = time.Now()
	if st.Status == core.StatusOffline {
		st.Status = core.StatusOnline
	}
	s.students[id] = st
	callbacks := s.snapshotListenersLocked()
	s.mu.Unlock()

	notifyListeners(callbacks)
	return nil
}

func (s *Service) TouchStudent(id string) {
	s.mu.Lock()
	st, ok := s.students[id]
	if !ok {
		s.mu.Unlock()
		return
	}
	st.LastSeen = time.Now()
	var callbacks []func()
	if st.Status == core.StatusOffline {
		st.Status = core.StatusOnline
		callbacks = s.snapshotListenersLocked()
	}
	s.students[id] = st
	s.mu.Unlock()

	notifyListeners(callbacks)
}

func (s *Service) MarkOfflineStale(timeout time.Duration) []string {
	cutoff := time.Now().Add(-timeout)
	s.mu.Lock()
	var marked []string
	for id, st := range s.students {
		if st.Status != core.StatusOffline && st.LastSeen.Before(cutoff) {
			st.Status = core.StatusOffline
			st.HandRaised = false
			s.syncHandQueueLocked(id, false)
			s.students[id] = st
			marked = append(marked, id)
		}
	}
	var callbacks []func()
	if len(marked) > 0 {
		sort.Strings(marked)
		callbacks = s.snapshotListenersLocked()
	}
	s.mu.Unlock()

	notifyListeners(callbacks)
	return marked
}

func (s *Service) ListStudents() []core.StudentInfo {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]core.StudentInfo, 0, len(s.students))
	for _, st := range s.students {
		out = append(out, st)
	}
	sort.Slice(out, func(left, right int) bool {
		if out[left].Seat.Row != out[right].Seat.Row {
			return out[left].Seat.Row < out[right].Seat.Row
		}
		if out[left].Seat.Col != out[right].Seat.Col {
			return out[left].Seat.Col < out[right].Seat.Col
		}
		if out[left].Name != out[right].Name {
			return out[left].Name < out[right].Name
		}
		return out[left].ID < out[right].ID
	})
	return out
}

func (s *Service) SetGroups(groups []core.GroupInfo) {
	s.mu.Lock()
	s.groups = make(map[string]core.GroupInfo, len(groups))
	for id, st := range s.students {
		st.GroupID = ""
		s.students[id] = st
	}
	for _, grp := range groups {
		members := make([]string, len(grp.Members))
		copy(members, grp.Members)
		grp.Members = members
		if grp.LeaderID == "" && len(grp.Members) > 0 {
			grp.LeaderID = grp.Members[0]
		}
		s.groups[grp.ID] = grp
		for _, memberID := range grp.Members {
			if st, ok := s.students[memberID]; ok {
				st.GroupID = grp.ID
				s.students[memberID] = st
			}
		}
	}
	callbacks := s.snapshotListenersLocked()
	s.mu.Unlock()

	notifyListeners(callbacks)
}

func (s *Service) ListGroups() []core.GroupInfo {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]core.GroupInfo, 0, len(s.groups))
	for _, grp := range s.groups {
		members := make([]string, len(grp.Members))
		copy(members, grp.Members)
		grp.Members = members
		out = append(out, grp)
	}
	sort.Slice(out, func(left, right int) bool {
		if out[left].Name != out[right].Name {
			return out[left].Name < out[right].Name
		}
		return out[left].ID < out[right].ID
	})
	return out
}

func (s *Service) GetGroup(id string) (core.GroupInfo, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	grp, ok := s.groups[id]
	if !ok {
		return core.GroupInfo{}, false
	}
	members := make([]string, len(grp.Members))
	copy(members, grp.Members)
	grp.Members = members
	return grp, true
}

func (s *Service) AddGroupMember(groupID, studentID string) error {
	s.mu.Lock()
	grp, ok := s.groups[groupID]
	if !ok {
		s.mu.Unlock()
		return ErrGroupNotFound
	}
	st, hasStudent := s.students[studentID]
	if !hasStudent {
		s.mu.Unlock()
		return ErrStudentNotFound
	}

	if st.GroupID != "" && st.GroupID != groupID {
		s.removeMemberFromGroupLocked(st.GroupID, studentID)
	}

	alreadyMember := false
	for _, memberID := range grp.Members {
		if memberID == studentID {
			alreadyMember = true
			break
		}
	}
	if !alreadyMember {
		grp.Members = append(grp.Members, studentID)
	}
	if grp.LeaderID == "" {
		grp.LeaderID = studentID
	}
	s.groups[groupID] = grp
	st.GroupID = groupID
	s.students[studentID] = st
	callbacks := s.snapshotListenersLocked()
	s.mu.Unlock()

	notifyListeners(callbacks)
	return nil
}

func (s *Service) RemoveGroupMember(groupID, studentID string) error {
	s.mu.Lock()
	if _, ok := s.groups[groupID]; !ok {
		s.mu.Unlock()
		return ErrGroupNotFound
	}
	s.removeMemberFromGroupLocked(groupID, studentID)
	if st, ok := s.students[studentID]; ok && st.GroupID == groupID {
		st.GroupID = ""
		s.students[studentID] = st
	}
	callbacks := s.snapshotListenersLocked()
	s.mu.Unlock()

	notifyListeners(callbacks)
	return nil
}

func (s *Service) removeMemberFromGroupLocked(groupID, studentID string) {
	grp, ok := s.groups[groupID]
	if !ok {
		return
	}
	filtered := make([]string, 0, len(grp.Members))
	for _, memberID := range grp.Members {
		if memberID != studentID {
			filtered = append(filtered, memberID)
		}
	}
	grp.Members = filtered
	if grp.LeaderID == studentID {
		if len(filtered) > 0 {
			grp.LeaderID = filtered[0]
		} else {
			grp.LeaderID = ""
		}
	}
	s.groups[groupID] = grp
}

func (s *Service) SetGroupLeader(groupID, leaderID string) error {
	s.mu.Lock()
	grp, ok := s.groups[groupID]
	if !ok {
		s.mu.Unlock()
		return ErrGroupNotFound
	}
	if leaderID != "" {
		found := false
		for _, memberID := range grp.Members {
			if memberID == leaderID {
				found = true
				break
			}
		}
		if !found {
			s.mu.Unlock()
			return ErrInvalidLeader
		}
	}
	grp.LeaderID = leaderID
	s.groups[groupID] = grp
	callbacks := s.snapshotListenersLocked()
	s.mu.Unlock()

	notifyListeners(callbacks)
	return nil
}

func (s *Service) UpdateSeat(studentID string, pos core.SeatPos) {
	s.mu.Lock()
	st, ok := s.students[studentID]
	if !ok {
		s.mu.Unlock()
		return
	}
	st.Seat = pos
	s.students[studentID] = st
	callbacks := s.snapshotListenersLocked()
	s.mu.Unlock()

	notifyListeners(callbacks)
}

func (s *Service) SwapSeats(studentA, studentB string) error {
	s.mu.Lock()
	first, okA := s.students[studentA]
	second, okB := s.students[studentB]
	if !okA || !okB {
		s.mu.Unlock()
		return ErrStudentNotFound
	}
	first.Seat, second.Seat = second.Seat, first.Seat
	s.students[studentA] = first
	s.students[studentB] = second
	callbacks := s.snapshotListenersLocked()
	s.mu.Unlock()

	notifyListeners(callbacks)
	return nil
}

func (s *Service) AutoLayoutSeats(rows, cols int) {
	s.mu.Lock()
	count := len(s.students)
	if count == 0 {
		s.mu.Unlock()
		return
	}
	if cols <= 0 {
		if rows > 0 {
			cols = int(math.Ceil(float64(count) / float64(rows)))
		} else {
			cols = int(math.Ceil(math.Sqrt(float64(count))))
		}
		if cols < 1 {
			cols = 1
		}
	}

	ordered := make([]core.StudentInfo, 0, count)
	for _, st := range s.students {
		ordered = append(ordered, st)
	}
	sort.Slice(ordered, func(left, right int) bool {
		if ordered[left].StudentNo != ordered[right].StudentNo {
			return ordered[left].StudentNo < ordered[right].StudentNo
		}
		if ordered[left].Name != ordered[right].Name {
			return ordered[left].Name < ordered[right].Name
		}
		return ordered[left].ID < ordered[right].ID
	})

	for idx, st := range ordered {
		st.Seat = core.SeatPos{
			Row: idx / cols,
			Col: idx % cols,
		}
		s.students[st.ID] = st
	}
	callbacks := s.snapshotListenersLocked()
	s.mu.Unlock()

	notifyListeners(callbacks)
}

func (s *Service) StartCheckIn() {
	s.mu.Lock()
	s.checkInActive = true
	s.checkInStartedAt = time.Now()
	for id, st := range s.students {
		st.CheckedIn = false
		s.students[id] = st
	}
	callbacks := s.snapshotListenersLocked()
	s.mu.Unlock()

	notifyListeners(callbacks)
}

func (s *Service) EndCheckIn() {
	s.mu.Lock()
	s.checkInActive = false
	callbacks := s.snapshotListenersLocked()
	s.mu.Unlock()

	notifyListeners(callbacks)
}

func (s *Service) SubmitCheckIn(studentID, name, studentNo string) {
	if studentID == "" {
		return
	}
	s.mu.Lock()
	st, ok := s.students[studentID]
	if !ok {
		st = core.StudentInfo{
			ID:     studentID,
			Status: core.StatusOnline,
		}
	}
	if name != "" {
		st.Name = name
	}
	if studentNo != "" {
		st.StudentNo = studentNo
	}
	st.CheckedIn = true
	st.LastSeen = time.Now()
	if st.Status == "" || st.Status == core.StatusOffline {
		st.Status = core.StatusOnline
	}
	s.students[studentID] = st
	callbacks := s.snapshotListenersLocked()
	s.mu.Unlock()

	notifyListeners(callbacks)
}

func (s *Service) CheckInSummary() CheckInSummary {
	s.mu.RLock()
	defer s.mu.RUnlock()
	summary := CheckInSummary{
		Active:     s.checkInActive,
		StartedAt:  s.checkInStartedAt,
		Total:      len(s.students),
		CheckedIDs: make([]string, 0),
		PendingIDs: make([]string, 0),
	}
	for _, st := range s.students {
		if st.CheckedIn {
			summary.CheckedIn++
			summary.CheckedIDs = append(summary.CheckedIDs, st.ID)
		} else {
			summary.Pending++
			summary.PendingIDs = append(summary.PendingIDs, st.ID)
		}
	}
	sort.Strings(summary.CheckedIDs)
	sort.Strings(summary.PendingIDs)
	return summary
}

func (s *Service) RaiseHand(studentID string, raised bool) {
	s.mu.Lock()
	st, ok := s.students[studentID]
	if !ok {
		s.mu.Unlock()
		return
	}
	st.HandRaised = raised
	if raised {
		if st.Status == "" || st.Status == core.StatusOnline {
			st.Status = core.StatusHandRaised
		}
	} else if st.Status == core.StatusHandRaised {
		st.Status = core.StatusOnline
	}
	s.students[studentID] = st
	s.syncHandQueueLocked(studentID, raised)
	callbacks := s.snapshotListenersLocked()
	s.mu.Unlock()

	notifyListeners(callbacks)
}

func (s *Service) syncHandQueueLocked(studentID string, raised bool) {
	pos := -1
	for idx, id := range s.handQueue {
		if id == studentID {
			pos = idx
			break
		}
	}
	if raised {
		if pos == -1 {
			s.handQueue = append(s.handQueue, studentID)
		}
		return
	}
	if pos != -1 {
		s.handQueue = append(s.handQueue[:pos], s.handQueue[pos+1:]...)
	}
}

func (s *Service) ListRaisedHands() []core.StudentInfo {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]core.StudentInfo, 0, len(s.handQueue))
	for _, id := range s.handQueue {
		if st, ok := s.students[id]; ok && st.HandRaised {
			out = append(out, st)
		}
	}
	return out
}

func (s *Service) ClearHands() {
	s.mu.Lock()
	for id, st := range s.students {
		if st.HandRaised || st.Status == core.StatusHandRaised {
			st.HandRaised = false
			if st.Status == core.StatusHandRaised {
				st.Status = core.StatusOnline
			}
			s.students[id] = st
		}
	}
	s.handQueue = nil
	callbacks := s.snapshotListenersLocked()
	s.mu.Unlock()

	notifyListeners(callbacks)
}

func (s *Service) AddChat(msg core.ChatMessage) {
	if msg.ID == "" {
		msg.ID = fmt.Sprintf("chat-%d", s.nextChatID.Add(1))
	}
	if msg.CreatedAt.IsZero() {
		msg.CreatedAt = time.Now()
	}

	s.mu.Lock()
	s.chats = append(s.chats, msg)
	callbacks := s.snapshotListenersLocked()
	s.mu.Unlock()

	notifyListeners(callbacks)
}

func (s *Service) BroadcastAnnouncement(senderID, senderName, text string) core.ChatMessage {
	msg := core.ChatMessage{
		ID:        fmt.Sprintf("chat-%d", s.nextChatID.Add(1)),
		SenderID:  senderID,
		Sender:    senderName,
		TargetID:  "all",
		Text:      text,
		CreatedAt: time.Now(),
	}
	s.AddChat(msg)
	return msg
}

func (s *Service) ListChats() []core.ChatMessage {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]core.ChatMessage, len(s.chats))
	copy(out, s.chats)
	return out
}
