package connection_health

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"
	"time"
	"transithub/backend/internal/modules/upstream"
)

func cloneModelMapping(m map[string]string) map[string]string {
	r := map[string]string{}
	for k, v := range m {
		r[k] = v
	}
	return r
}
func modelMappingEntries(m map[string]string, state string) []ModelControlEntry {
	keys := []string{}
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	r := []ModelControlEntry{}
	for _, k := range keys {
		r = append(r, ModelControlEntry{k, m[k], state})
	}
	return r
}
func modelControlSupports(a upstream.AdminGroupAccountInfo, key string) (bool, bool) {
	if a.Type != "apikey" || (a.Platform != "openai" && a.Platform != "anthropic") {
		return false, false
	}
	if a.Platform == "openai" && a.OpenAIPassthrough {
		return true, true
	}
	if !a.ModelMappingKnown {
		return false, false
	}
	if len(a.ModelMapping) == 0 {
		return true, true
	}
	if _, ok := a.ModelMapping[key]; ok {
		return true, true
	}
	for k := range a.ModelMapping {
		if strings.HasSuffix(k, "*") && strings.HasPrefix(key, strings.TrimSuffix(k, "*")) {
			return true, true
		}
	}
	return false, true
}

type modelClosePlan struct {
	State, ReasonKey      string
	Entries, AfterMapping map[string]string
}

func planModelClose(d upstream.Sub2APIModelControlAccount, model string) modelClosePlan {
	p := modelClosePlan{State: "not_isolatable", Entries: map[string]string{}, AfterMapping: cloneModelMapping(d.ModelMapping)}
	switch {
	case d.Type != "apikey" || (d.Platform != "openai" && d.Platform != "anthropic"):
		p.ReasonKey = modelControlError("AccountTypeUnsupported")
		return p
	case d.Platform == "openai" && d.OpenAIPassthrough:
		p.ReasonKey = modelControlError("OpenAIPassthrough")
		return p
	case !d.ModelMappingKnown:
		p.ReasonKey = modelControlError("AccountReadFailed")
		return p
	case len(d.ModelMapping) == 0:
		p.ReasonKey = modelControlError("EmptyMapping")
		return p
	}
	for k, v := range d.ModelMapping {
		if v == model && strings.HasSuffix(k, "*") {
			p.ReasonKey = modelControlError("WildcardMapping")
			return p
		}
		if v == model && !strings.HasSuffix(k, "*") {
			p.Entries[k] = v
			delete(p.AfterMapping, k)
		}
	}
	if len(p.Entries) == 0 {
		p.State, p.ReasonKey = "not_provided", modelControlError("NotProvided")
		return p
	}
	for pattern := range p.AfterMapping {
		if !strings.HasSuffix(pattern, "*") {
			continue
		}
		for k := range p.Entries {
			if strings.HasPrefix(k, strings.TrimSuffix(pattern, "*")) {
				p.ReasonKey = modelControlError("WildcardFallback")
				return p
			}
		}
	}
	if len(p.AfterMapping) == 0 {
		p.State, p.ReasonKey = "last_model", modelControlError("LastModel")
		return p
	}
	p.State = "serving"
	return p
}

type modelRestorePlan struct {
	ReasonKey                                            string
	Entries, AfterMapping, ManualRestored, ManualChanged map[string]string
	NoRemoteWrite                                        bool
}

func planModelRestore(d upstream.Sub2APIModelControlAccount, closed map[string]string) modelRestorePlan {
	p := modelRestorePlan{Entries: map[string]string{}, AfterMapping: cloneModelMapping(d.ModelMapping), ManualRestored: map[string]string{}, ManualChanged: map[string]string{}}
	switch {
	case d.Type != "apikey" || (d.Platform != "openai" && d.Platform != "anthropic"):
		p.ReasonKey = modelControlError("RestoreTypeChanged")
		return p
	case d.Platform == "openai" && d.OpenAIPassthrough:
		p.ReasonKey = modelControlError("RestorePassthrough")
		return p
	case !d.ModelMappingKnown:
		p.ReasonKey = modelControlError("AccountReadFailed")
		return p
	case len(d.ModelMapping) == 0:
		p.ReasonKey = modelControlError("RestoreMappingEmpty")
		return p
	}
	for k, v := range closed {
		if actual, ok := d.ModelMapping[k]; ok {
			if actual == v {
				p.ManualRestored[k] = v
			} else {
				p.ManualChanged[k] = v
			}
		} else {
			p.Entries[k] = v
			p.AfterMapping[k] = v
		}
	}
	p.NoRemoteWrite = len(p.Entries) == 0
	return p
}
func modelControlPlanFingerprint(op string, mapping, entries map[string]string, schedulable ...*bool) string {
	body := struct {
		Operation        string
		Mapping, Entries map[string]string
		Schedulable      *bool
	}{Operation: op, Mapping: mapping, Entries: entries}
	if op == "close_account" && len(schedulable) > 0 {
		body.Schedulable = schedulable[0]
	}
	b, _ := json.Marshal(body)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

type modelControlAttribution struct {
	ClosedEntries map[string]string
	Entries       []ModelControlEntry
	Outcome       string
	ManualEntries []ModelControlEntry
}

func attributeModelControlEntries(mapping, closed map[string]string, pending *modelControlPending) modelControlAttribution {
	a := modelControlAttribution{ClosedEntries: cloneModelMapping(closed), Entries: []ModelControlEntry{}, ManualEntries: []ModelControlEntry{}}
	for _, e := range modelMappingEntries(closed, "still_closed") {
		if actual, ok := mapping[e.Key]; ok {
			delete(a.ClosedEntries, e.Key)
			if actual == e.Value {
				e.State = "manually_restored"
			} else {
				e.State = "manually_changed"
			}
			a.ManualEntries = append(a.ManualEntries, e)
		}
		a.Entries = append(a.Entries, e)
	}
	if pending == nil {
		return a
	}
	a.Entries = []ModelControlEntry{}
	success := 0
	for _, e := range modelMappingEntries(pending.Entries, "") {
		actual, exists := mapping[e.Key]
		if pending.Operation == "close" {
			if !exists {
				a.ClosedEntries[e.Key] = e.Value
				e.State = "closed"
				success++
			} else if actual == e.Value {
				e.State = "not_deleted"
			} else {
				e.State = "manually_changed"
				delete(a.ClosedEntries, e.Key)
				a.ManualEntries = append(a.ManualEntries, e)
			}
		} else {
			if !exists {
				e.State = "still_closed"
				if pending.Operation == "add" {
					e.State = "not_added"
				}
			} else {
				delete(a.ClosedEntries, e.Key)
				if actual == e.Value {
					e.State = "restored"
					if pending.Operation == "add" {
						e.State = "added"
					}
					success++
				} else {
					e.State = "manually_changed"
					a.ManualEntries = append(a.ManualEntries, e)
				}
			}
		}
		a.Entries = append(a.Entries, e)
	}
	a.Outcome = "partial"
	if success == len(pending.Entries) {
		a.Outcome = "succeeded"
	} else if success == 0 {
		a.Outcome = "failed"
	}
	return a
}
func modelControlTargetUsable(a upstream.AdminGroupAccountInfo) bool {
	return modelControlActiveStatus(a.Status) && a.Schedulable != nil && *a.Schedulable
}
func modelControlSourceSchedulable(a upstream.AdminGroupAccountInfo, key string, now time.Time) bool {
	if !modelControlTargetUsable(a) || !upstream.Sub2APIDeadlineExpired(a.TempUnschedulableUntil, a.TempUnschedulableKnown, now) || !upstream.Sub2APIDeadlineExpired(a.OverloadUntil, a.OverloadKnown, now) || !upstream.Sub2APIDeadlineExpired(a.RateLimitResetAt, a.RateLimitKnown, now) {
		return false
	}
	if a.AutoPauseOnExpired == nil || !a.ExpiresAtKnown || (*a.AutoPauseOnExpired && a.ExpiresAt != nil && !a.ExpiresAt.After(now)) || !a.QuotaKnown || !a.ModelRateLimitsKnown {
		return false
	}
	for _, q := range [][2]*float64{{a.QuotaLimit, a.QuotaUsed}, {a.QuotaDailyLimit, a.QuotaDailyUsed}, {a.QuotaWeeklyLimit, a.QuotaWeeklyUsed}} {
		if q[0] != nil && *q[0] > 0 && (q[1] == nil || *q[1] >= *q[0]) {
			return false
		}
	}
	upstreamModel := modelControlMappedModel(a.ModelMapping, key)
	for _, limit := range a.ModelRateLimits {
		if limit.Model == key || (upstreamModel != "" && limit.Model == upstreamModel) {
			if !upstream.Sub2APIDeadlineExpired(limit.ResetAt, limit.Known, now) {
				return false
			}
		}
	}
	return true
}
func countModelSources(inventory adminWorkspaceInventory, target upstream.AdminGroupAccountInfo, keys map[string]string, healthPending map[string]bool, c3Reserved map[string]map[string]bool, now time.Time) []modelSourceCount {
	result := []modelSourceCount{}
	for _, g := range inventory.groups {
		member := false
		for _, a := range g.accounts {
			if a.ID == target.ID {
				member = true
				break
			}
		}
		if !member {
			continue
		}
		for _, entry := range modelMappingEntries(keys, "") {
			c := modelSourceCount{GroupID: g.group.ID, GroupName: g.group.Name, Key: entry.Key}
			if adminInventoryComplete(inventory) {
				n := 0
				seen := map[string]bool{}
				for _, a := range g.accounts {
					if seen[a.ID] || a.ID == target.ID || a.Platform != target.Platform || a.Type != "apikey" {
						continue
					}
					seen[a.ID] = true
					if healthPending[a.ID] || c3Reserved[a.ID]["*"] || c3Reserved[a.ID][entry.Key] {
						continue
					}
					supported, known := modelControlSupports(a, entry.Key)
					if supported && known && modelControlSourceSchedulable(a, entry.Key, now) {
						n++
					}
				}
				c.Count = &n
				c.OK = n >= 1
			}
			result = append(result, c)
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].GroupID == result[j].GroupID {
			return result[i].Key < result[j].Key
		}
		return result[i].GroupID < result[j].GroupID
	})
	return result
}

func modelControlActiveStatus(status string) bool {
	v, known := normalizeFloorTargetStatus("sub2api", status)
	return known && v == "active"
}

func modelControlMappedModel(mapping map[string]string, key string) string {
	if value, ok := mapping[key]; ok {
		return value
	}
	best := ""
	value := key
	for pattern, mapped := range mapping {
		if strings.HasSuffix(pattern, "*") && strings.HasPrefix(key, strings.TrimSuffix(pattern, "*")) && (len(pattern) > len(best) || len(pattern) == len(best) && pattern < best) {
			best = pattern
			value = mapped
		}
	}
	return value
}

type modelAddPlan struct {
	ReasonKey                       string
	Entries, AfterMapping, Blocking map[string]string
}

func planModelAdd(d upstream.Sub2APIModelControlAccount, model string) modelAddPlan {
	p := modelAddPlan{Entries: map[string]string{}, AfterMapping: cloneModelMapping(d.ModelMapping)}
	switch {
	case d.Type != "apikey" || (d.Platform != "openai" && d.Platform != "anthropic"):
		p.ReasonKey = modelControlError("AddTypeUnsupported")
	case d.Platform == "openai" && d.OpenAIPassthrough:
		p.ReasonKey = modelControlError("AddPassthrough")
	case !d.ModelMappingKnown:
		p.ReasonKey = modelControlError("AccountReadFailed")
	case len(d.ModelMapping) == 0:
		p.ReasonKey = modelControlError("AddMappingEmpty")
	}
	if p.ReasonKey != "" {
		return p
	}
	for _, entry := range modelMappingEntries(d.ModelMapping, "") {
		if entry.Value == model {
			p.ReasonKey = modelControlError("AlreadyProvided")
			return p
		}
	}
	if value, exists := d.ModelMapping[model]; exists {
		p.ReasonKey = modelControlError("AddKeyConflict")
		p.Blocking = map[string]string{model: value}
		return p
	}
	for _, entry := range modelMappingEntries(d.ModelMapping, "") {
		if strings.HasSuffix(entry.Key, "*") && strings.HasPrefix(model, strings.TrimSuffix(entry.Key, "*")) {
			p.ReasonKey = modelControlError("AddWildcardConflict")
			p.Blocking = map[string]string{entry.Key: entry.Value}
			return p
		}
	}
	p.Entries[model] = model
	p.AfterMapping[model] = model
	return p
}
