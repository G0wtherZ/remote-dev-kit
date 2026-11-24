package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"time"
)

type bSchedule struct {
	ID          string    `json:"id"`
	Device      string    `json:"device"`
	Project     string    `json:"project"`
	Prompt      string    `json:"prompt"`
	Next        time.Time `json:"next"`
	Interval    int64     `json:"interval_seconds"`
	Enabled     bool      `json:"enabled"`
	LastSession string    `json:"last_session,omitempty"`
	LastResult  string    `json:"last_result,omitempty"`
}

func (w *bWeb) readSchedules() []bSchedule {
	out := []bSchedule{}
	b, e := os.ReadFile("/data/config/schedules.json")
	if e == nil {
		_ = json.Unmarshal(b, &out)
	}
	return out
}
func (w *bWeb) saveSchedules(values []bSchedule) error {
	b, e := json.MarshalIndent(values, "", "  ")
	if e != nil {
		return e
	}
	return atomicWrite("/data/config/schedules.json", b, 0600)
}
func (w *bWeb) schedules(out http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(out, "method", 405)
		return
	}
	var schedule bSchedule
	if e := bDecode(out, r, &schedule); e != nil {
		bWebError(out, e)
		return
	}
	if schedule.ID == "" {
		schedule.ID = bRandom(6)
	}
	if !bIDPattern.MatchString(schedule.ID) || !bIDPattern.MatchString(schedule.Device) || schedule.Project == "" || len(schedule.Prompt) == 0 || len(schedule.Prompt) > 16384 || schedule.Interval < 0 || schedule.Interval > 365*86400 || schedule.Interval > 0 && schedule.Interval < 60 {
		bWebError(out, errors.New("任务需有效设备/项目/提示，重复间隔至少 60 秒"))
		return
	}
	if schedule.Next.IsZero() {
		bWebError(out, errors.New("请选择首次执行时间"))
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	values := w.readSchedules()
	found := false
	for i, v := range values {
		if v.ID == schedule.ID {
			schedule.LastResult = v.LastResult
			schedule.LastSession = v.LastSession
			values[i] = schedule
			found = true
		}
	}
	if !found {
		if len(values) >= 64 {
			bWebError(out, errors.New("任务上限为 64 个，请停用/修改现有任务"))
			return
		}
		values = append(values, schedule)
	}
	if e := w.saveSchedules(values); e != nil {
		bWebError(out, e)
		return
	}
	bJSON(out, schedule)
}
func (w *bWeb) scheduler(ctx context.Context) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			w.dispatchDue(now)
		}
	}
}
func (w *bWeb) dispatchDue(now time.Time) {
	w.mu.Lock()
	defer w.mu.Unlock()
	values := w.readSchedules()
	changed := false
	for i := range values {
		s := &values[i]
		if !s.Enabled || now.Before(s.Next) {
			continue
		}
		late := now.Sub(s.Next) > 10*time.Minute
		// Reserve before launching: an interrupted dispatch is recorded and never replayed blindly.
		s.LastSession = "b-" + s.Device + "-" + bRandom(6)
		s.LastResult = "dispatch reserved; outcome pending"
		if s.Interval == 0 {
			s.Enabled = false
		} else {
			for !s.Next.After(now) {
				s.Next = s.Next.Add(time.Duration(s.Interval) * time.Second)
			}
		}
		if e := w.saveSchedules(values); e != nil {
			return
		}
		changed = true
		if late {
			s.LastResult = "skipped: more than 10 minutes late"
			continue
		}
		id := s.LastSession[len(s.LastSession)-12:]
		_, e := bCreateSession(bSessionRequest{ID: id, DeviceID: s.Device, Project: s.Project, Kind: "claude", Args: []string{s.Prompt}})
		if e != nil {
			s.LastResult = e.Error()
		} else {
			s.LastResult = "started; native approvals remain in terminal"
		}
	}
	if changed {
		_ = w.saveSchedules(values)
	}
}
