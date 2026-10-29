package notifications

import "time"

// SignupRiskSchedule is stored with the reminder switch, never with upstream settings.
type SignupRiskSchedule struct {
	WorkStart string `json:"workStart"`
	WorkEnd   string `json:"workEnd"`
	TimeZone  string `json:"timeZone"`
}

type signupRiskScheduleError string

func (e signupRiskScheduleError) Error() string { return string(e) }

func DefaultSignupRiskSchedule() SignupRiskSchedule {
	return SignupRiskSchedule{WorkStart: "09:00", WorkEnd: "18:00", TimeZone: "Asia/Singapore"}
}
func signupRiskClock(value string) (int, bool) {
	if len(value) != 5 || value[2] != ':' {
		return 0, false
	}
	for _, i := range []int{0, 1, 3, 4} {
		if value[i] < '0' || value[i] > '9' {
			return 0, false
		}
	}
	h, m := int(value[0]-'0')*10+int(value[1]-'0'), int(value[3]-'0')*10+int(value[4]-'0')
	return h*60 + m, h < 24 && m < 60
}
func (s SignupRiskSchedule) Validate() error {
	start, a := signupRiskClock(s.WorkStart)
	end, b := signupRiskClock(s.WorkEnd)
	if !a || !b {
		return signupRiskScheduleError("注册提醒的工作开始和结束时间必须完整填写为00:00至23:59的时:分；请重新加载页面并填写时间，原设置没有改变")
	}
	if start >= end {
		return signupRiskScheduleError("注册提醒的工作开始时间必须早于结束时间，本次不支持跨午夜；请调整时间，原设置没有改变")
	}
	if s.TimeZone != "Asia/Singapore" && s.TimeZone != "Asia/Tokyo" && s.TimeZone != "UTC" {
		return signupRiskScheduleError("注册提醒时区必须选择新加坡（UTC+8）、东京（UTC+9）或UTC；请重新加载页面并选择时区，原设置没有改变")
	}
	return nil
}

// Window returns an exclusive end and the next future daily start. Enabled is
// handled by the caller; the schedule itself never changes persisted state.
func (s SignupRiskSchedule) Window(now time.Time) (activeUntil, nextStartAt *time.Time, err error) {
	if err = s.Validate(); err != nil {
		return
	}
	offset := 0
	if s.TimeZone == "Asia/Singapore" {
		offset = 8 * 60 * 60
	} else if s.TimeZone == "Asia/Tokyo" {
		offset = 9 * 60 * 60
	}
	loc := time.FixedZone(s.TimeZone, offset)
	local := now.In(loc)
	startMinute, _ := signupRiskClock(s.WorkStart)
	endMinute, _ := signupRiskClock(s.WorkEnd)
	start := time.Date(local.Year(), local.Month(), local.Day(), startMinute/60, startMinute%60, 0, 0, loc)
	end := time.Date(local.Year(), local.Month(), local.Day(), endMinute/60, endMinute%60, 0, 0, loc)
	if !now.Before(start) && now.Before(end) {
		instant := end.UTC()
		activeUntil = &instant
	}
	if !now.Before(start) {
		start = start.AddDate(0, 0, 1)
	}
	instant := start.UTC()
	nextStartAt = &instant
	return
}
func (p *SignupRiskPreferences) at(now time.Time) error {
	p.ServerTime = now.UTC()
	p.ActiveUntil = nil
	p.NextStartAt = nil
	if err := p.SignupRiskSchedule.Validate(); err != nil {
		return err
	}
	if !p.Enabled {
		return nil
	}
	var err error
	p.ActiveUntil, p.NextStartAt, err = p.SignupRiskSchedule.Window(now)
	return err
}
