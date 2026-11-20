package prefixcheck

import (
	"encoding/json"
	"slices"
	"sort"
	"time"
)

const (
	PeerWindow          = 14 * 24 * time.Hour
	PeerLimit           = 20
	knowledgeMarginDays = 60
	minKnowers          = 2
)

type KnowledgeRow struct{ ID, Event, Kind, HappenedAt string }

type ReportInput struct {
	ReportID, AccountID, Model, Status string
	CreatedAtMs, UpdatedAtMs           int64
	Prefix                             json.RawMessage
	PrefixWrongType                    bool
	KnowledgeRows                      []KnowledgeRow
	Vision                             json.RawMessage
}

type ItemIndex struct {
	ItemID        string
	OrderIndex    int
	CompletedAtMs int64
}

type IdentityInput struct {
	ItemID     string
	OrderIndex int
	Status     string
	Earned     *int64
	Max        *int64
}

type PeerInput struct {
	ReportID, AccountName, Status string
	CreatedAtMs                   int64
	Prefix                        json.RawMessage
	KnowledgeRows                 []KnowledgeRow
	Vision                        json.RawMessage
}

type CrossInput struct {
	Report     ReportInput
	Items      []ItemIndex
	Injections []InjectionItem
	Identity   *IdentityInput
	Peers      []PeerInput
}

type Result struct {
	Version                    int             `json:"version"`
	ReportID                   string          `json:"reportId"`
	Model                      string          `json:"model"`
	ReportStatus               string          `json:"reportStatus"`
	ReportUpdatedAtMs          int64           `json:"reportUpdatedAtMs"`
	Terminal                   bool            `json:"terminal"`
	PerItemChecked             bool            `json:"perItemChecked"`
	PrefixStatus               string          `json:"prefixStatus"`
	PrefixReason               string          `json:"prefixReason"`
	PrefixStale                bool            `json:"prefixStale"`
	Checked                    int             `json:"checked"`
	Reference                  *CrossReference `json:"reference"`
	ReferenceUnavailableReason string          `json:"referenceUnavailableReason"`
	InjectionCount             int             `json:"injectionCount"`
	InjectionPoints            int             `json:"injectionPoints"`
	InjectionReliable          *bool           `json:"injectionReliable"`
	InjectionError             string          `json:"injectionError"`
	Groups                     []RouteGroup    `json:"groups"`
	ExpectedPromptIDs          []string        `json:"expectedPromptIds"`
	ExpectedPromptNames        []string        `json:"expectedPromptNames"`
	SelfReport                 SelfReport      `json:"selfReport"`
	Knowledge                  Knowledge       `json:"knowledge"`
	Vision                     Vision          `json:"vision"`
	Codes                      []string        `json:"codes"`
	Grade                      string          `json:"grade"`
	Peers                      []Peer          `json:"peers"`
	TooLarge                   bool            `json:"-"`
}

type CrossReference struct {
	ItemID          string `json:"itemId"`
	ATotal          int64  `json:"aTotal"`
	AInput          int64  `json:"aInput"`
	ACache          int64  `json:"aCache"`
	Points          int    `json:"points"`
	DefaultTemplate bool   `json:"defaultTemplate"`
	APromptTokens   int    `json:"aPromptTokens"`
	Overhead        int64  `json:"overhead"`
	PrefixTokens    int64  `json:"prefixTokens"`
	Match           *Match `json:"match"`
}

type RouteGroup struct {
	Overhead        int64       `json:"overhead"`
	OverheadMax     int64       `json:"overheadMax"`
	PrefixTokens    int64       `json:"prefixTokens"`
	Count           int         `json:"count"`
	Items           []GroupItem `json:"items"`
	SameAsReference bool        `json:"sameAsReference"`
	Match           *Match      `json:"match"`
}

type GroupItem struct {
	ItemID     string `json:"itemId"`
	OrderIndex int    `json:"orderIndex"`
	Overhead   int64  `json:"overhead"`
}

type SelfReport struct {
	Present  bool   `json:"present"`
	ItemID   string `json:"itemId"`
	Status   string `json:"status"`
	Earned   *int64 `json:"earned"`
	Max      *int64 `json:"max"`
	ByPrompt bool   `json:"byPrompt"`
}

type Knowledge struct {
	Rows           int                     `json:"rows"`
	PeerReports    int                     `json:"peerReports"`
	Contradictions []KnowledgeDisagreement `json:"contradictions"`
}

type KnowledgeDisagreement struct {
	EventID string `json:"eventId"`
	Event   string `json:"event"`
	Date    string `json:"date"`
	KnownBy int    `json:"knownBy"`
}

type Vision struct {
	Status   string `json:"status"`
	PeerFull int    `json:"peerFull"`
}

type Peer struct {
	ReportID     string      `json:"reportId"`
	AccountName  string      `json:"accountName"`
	CreatedAtMs  int64       `json:"createdAtMs"`
	Status       string      `json:"status"`
	PrefixTokens *int64      `json:"prefixTokens"`
	LatestKnown  *KnownEvent `json:"latestKnown"`
	Vision       string      `json:"vision"`
}

type KnownEvent struct {
	Event string `json:"event"`
	Date  string `json:"date"`
}

func parsePrefix(raw json.RawMessage) (string, string, Analysis) {
	var empty Analysis
	var data map[string]any
	if raw == nil {
		return "none", "", empty
	}
	if !object(raw, &data) {
		return "invalid", "", empty
	}
	version, ok := integer(data["version"])
	status, _ := data["status"].(string)
	if !ok || version != 1 || (status != "ok" && status != "unavailable") {
		return "invalid", "", empty
	}
	if status == "unavailable" {
		reason, _ := data["reason"].(string)
		return status, reason, empty
	}
	computed, computedOK := integer(data["computedAtMs"])
	checked, checkedOK := integer(data["checked"])
	skipped, skippedOK := integer(data["skipped"])
	reason, reasonOK := data["referenceReason"].(string)
	pairs, pairsOK := data["overheads"].([]any)
	if data["tokenizer"] != TokenizerName || !computedOK || !checkedOK || checked < 0 || !skippedOK || skipped < 0 || !reasonOK || !pairsOK {
		return "invalid", "", empty
	}
	prefix := Analysis{Version: 1, Status: "ok", Tokenizer: TokenizerName, ComputedAtMs: computed, Checked: int(checked), Skipped: int(skipped), ReferenceReason: reason, Overheads: make([][2]int64, 0, len(pairs))}
	for _, rawPair := range pairs {
		pair, ok := rawPair.([]any)
		if !ok || len(pair) != 2 {
			return "invalid", "", empty
		}
		order, orderOK := integer(pair[0])
		overhead, overheadOK := integer(pair[1])
		if !orderOK || !overheadOK {
			return "invalid", "", empty
		}
		prefix.Overheads = append(prefix.Overheads, [2]int64{order, overhead})
	}
	if data["reference"] != nil {
		ref, ok := data["reference"].(map[string]any)
		item, itemOK := ref["itemId"].(string)
		total, totalOK := integer(ref["aTotal"])
		prompt, promptOK := integer(ref["aPromptTokens"])
		overhead, overheadOK := integer(ref["overhead"])
		order, orderOK := integer(ref["orderIndex"])
		if !ok || !itemOK || !totalOK || !promptOK || !overheadOK || !orderOK {
			return "invalid", "", empty
		}
		prefix.Reference = &Reference{ItemID: item, OrderIndex: int(order), ATotal: total, APromptTokens: int(prompt), Overhead: overhead}
	}
	return status, "", prefix
}

func prefixConsistent(prefix Analysis, in CrossInput, itemIDs map[int64]string) bool {
	if prefix.Checked != len(prefix.Overheads) || prefix.Checked+prefix.Skipped+len(in.Injections) != len(in.Items) {
		return false
	}
	injected := make(map[int64]bool, len(in.Injections))
	for _, item := range in.Injections {
		injected[int64(item.OrderIndex)] = true
	}
	for i, pair := range prefix.Overheads {
		if _, found := itemIDs[pair[0]]; !found || injected[pair[0]] || (i > 0 && prefix.Overheads[i-1][0] >= pair[0]) {
			return false
		}
	}
	wantReason := referenceProblem(in.Injections)
	if prefix.ReferenceReason != wantReason {
		return false
	}
	ref := prefix.Reference
	if wantReason != "" {
		return ref == nil
	}
	inj := in.Injections[0]
	return ref != nil && ref.ItemID == inj.ItemID && ref.OrderIndex == inj.OrderIndex &&
		ref.ATotal == inj.Points[0].Total && ref.Overhead == ref.ATotal-int64(ref.APromptTokens) &&
		ref.APromptTokens >= 1 && (injectionTemplate(inj.Template) != defaultTemplate || ref.APromptTokens == 1)
}

func makeGroups(overheads [][2]int64, itemIDs map[int64]string, reference *CrossReference) []RouteGroup {
	rows := slices.Clone(overheads)
	sort.Slice(rows, func(i, j int) bool {
		if rows[i][1] != rows[j][1] {
			return rows[i][1] < rows[j][1]
		}
		return rows[i][0] < rows[j][0]
	})
	groups := []RouteGroup{}
	for _, row := range rows {
		overhead := row[1]
		if len(groups) == 0 || overhead-groups[len(groups)-1].Overhead > SameRouteTolerance {
			tokens := PrefixOf(overhead)
			groups = append(groups, RouteGroup{Overhead: overhead, PrefixTokens: tokens, Match: MatchPrefix(tokens)})
		}
		g := &groups[len(groups)-1]
		g.OverheadMax = overhead
		g.Items = append(g.Items, GroupItem{itemIDs[row[0]], int(row[0]), overhead})
	}
	for i := range groups {
		g := &groups[i]
		sort.Slice(g.Items, func(i, j int) bool { return g.Items[i].OrderIndex < g.Items[j].OrderIndex })
		g.Count = len(g.Items)
		if reference != nil {
			delta := g.Overhead - reference.Overhead
			g.SameAsReference = max(delta, -delta) <= SameRouteTolerance
		}
	}
	return groups
}

func KnowledgeDate(value string) (string, bool) {
	if len(value) < 10 || value[:4] == "0000" {
		return "", false
	}
	date := value[:10]
	_, err := time.Parse(time.DateOnly, date)
	return date, err == nil
}

func contradictions(rows []KnowledgeRow, peers []PeerInput) []KnowledgeDisagreement {
	sorted := slices.Clone(rows)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].HappenedAt != sorted[j].HappenedAt {
			return sorted[i].HappenedAt < sorted[j].HappenedAt
		}
		return sorted[i].ID < sorted[j].ID
	})
	out := []KnowledgeDisagreement{}
	for _, row := range sorted {
		if row.Kind != "unknown" && row.Kind != "wrong" {
			continue
		}
		date, _ := time.Parse(time.DateOnly, row.HappenedAt)
		threshold := date.AddDate(0, 0, knowledgeMarginDays)
		knownBy, contradicted := 0, false
		for _, peer := range peers {
			knows, knowsLater := false, false
			for _, candidate := range peer.KnowledgeRows {
				if row.ID == candidate.ID && (candidate.Kind == "unknown" || candidate.Kind == "wrong") {
					contradicted = true
				}
				if candidate.Kind == "know" {
					knows = knows || row.ID == candidate.ID
					peerDate, _ := time.Parse(time.DateOnly, candidate.HappenedAt)
					knowsLater = knowsLater || !peerDate.Before(threshold)
				}
			}
			if knows && knowsLater {
				knownBy++
			}
		}
		if knownBy >= minKnowers && !contradicted {
			out = append(out, KnowledgeDisagreement{row.ID, row.Event, row.HappenedAt, knownBy})
		}
	}
	return out
}

func visionStatus(raw json.RawMessage) string {
	var values map[string]any
	if !object(raw, &values) || values["applicable"] == false || values["known"] != true {
		return "none"
	}
	points, pointsOK := integer(values["points"])
	maxPoints, maxOK := integer(values["max_points"])
	switch {
	case !pointsOK || !maxOK:
		return "none"
	case points == maxPoints:
		return "full"
	case points == 0:
		return "zero"
	default:
		return "partial"
	}
}

func comparePeer(in PeerInput) Peer {
	peer := Peer{ReportID: in.ReportID, AccountName: in.AccountName, CreatedAtMs: in.CreatedAtMs, Status: in.Status, Vision: visionStatus(in.Vision)}
	if status, _, p := parsePrefix(in.Prefix); status == "ok" && p.Reference != nil {
		tokens := PrefixOf(p.Reference.Overhead)
		peer.PrefixTokens = &tokens
	}
	var latest KnowledgeRow
	for _, row := range in.KnowledgeRows {
		if row.Kind == "know" && (row.HappenedAt > latest.HappenedAt || (row.HappenedAt == latest.HappenedAt && row.ID > latest.ID)) {
			latest = row
		}
	}
	if latest.HappenedAt != "" {
		peer.LatestKnown = &KnownEvent{latest.Event, latest.HappenedAt}
	}
	return peer
}

func CrossCheck(in CrossInput) Result {
	r := in.Report
	out := Result{
		Version: 1, ReportID: r.ReportID, Model: r.Model, ReportStatus: r.Status, ReportUpdatedAtMs: r.UpdatedAtMs,
		InjectionCount: len(in.Injections), ExpectedPromptIDs: ExpectedFor(r.Model), ExpectedPromptNames: []string{},
		Codes: []string{}, Peers: make([]Peer, 0, len(in.Peers)),
	}
	switch r.Status {
	case "done", "partial", "cancelled", "interrupted":
		out.Terminal = true
	}
	var maxCompleted int64
	itemIDs := make(map[int64]string, len(in.Items))
	for _, item := range in.Items {
		maxCompleted = max(maxCompleted, item.CompletedAtMs)
		itemIDs[int64(item.OrderIndex)] = item.ItemID
	}
	status, reason, prefix := parsePrefix(r.Prefix)
	switch {
	case r.PrefixWrongType:
		status, reason = "invalid", ""
	case status == "ok" && prefix.ComputedAtMs < maxCompleted:
		status = "stale"
	case status == "ok" && !prefixConsistent(prefix, in, itemIDs):
		status = "invalid"
	}
	out.PrefixStatus, out.PrefixReason = status, reason
	out.PrefixStale, out.PerItemChecked = status == "stale", status == "ok"

	var ref *Reference
	if out.PerItemChecked {
		ref, out.ReferenceUnavailableReason, out.Checked = prefix.Reference, prefix.ReferenceReason, prefix.Checked
	} else {
		out.ReferenceUnavailableReason = referenceProblem(in.Injections)
		if out.ReferenceUnavailableReason == "" {
			if injectionTemplate(in.Injections[0].Template) == defaultTemplate {
				ref = newReference(in.Injections[0], 1)
			} else {
				out.ReferenceUnavailableReason = oldCustomTemplate
			}
		}
	}
	if len(in.Injections) == 1 {
		inj := in.Injections[0]
		out.InjectionPoints, out.InjectionReliable, out.InjectionError = len(inj.Points), inj.Reliable, inj.Error
		if ref != nil {
			pt := PrefixOf(ref.Overhead)
			out.Reference = &CrossReference{
				ItemID: ref.ItemID, ATotal: ref.ATotal, AInput: inj.Points[0].Input, ACache: ref.ATotal - inj.Points[0].Input,
				Points: len(inj.Points), DefaultTemplate: injectionTemplate(inj.Template) == defaultTemplate,
				APromptTokens: ref.APromptTokens, Overhead: ref.Overhead, PrefixTokens: pt, Match: MatchPrefix(pt),
			}
		}
	}
	if out.PerItemChecked {
		out.Groups = makeGroups(prefix.Overheads, itemIDs, out.Reference)
	}
	mixed := len(out.Groups) >= 2 || (!out.PerItemChecked && out.InjectionPoints >= 2 && out.InjectionReliable != nil && !*out.InjectionReliable)
	if mixed {
		out.Codes = append(out.Codes, "mixed_routes")
	}
	for _, id := range out.ExpectedPromptIDs {
		for _, prompt := range loadPrompts() {
			if prompt.ID == id {
				out.ExpectedPromptNames = append(out.ExpectedPromptNames, prompt.Name)
				break
			}
		}
	}
	mismatch := func(match *Match) bool {
		return match != nil && (match.ID == "sub2api-placeholder" || (len(out.ExpectedPromptIDs) > 0 && !slices.Contains(out.ExpectedPromptIDs, match.ID)))
	}
	var referenceMatch *Match
	if out.Reference != nil {
		referenceMatch = out.Reference.Match
	}
	promptMismatch := mismatch(referenceMatch)
	for _, group := range out.Groups {
		promptMismatch = promptMismatch || mismatch(group.Match)
	}
	if promptMismatch {
		out.Codes = append(out.Codes, "prompt_mismatch")
	}
	selfValid, selfGPT5 := false, false
	if ident := in.Identity; ident != nil {
		out.SelfReport = SelfReport{Present: true, ItemID: ident.ItemID, Status: ident.Status, Earned: ident.Earned, Max: ident.Max}
		selfValid = ident.Status == "known" && ident.Earned != nil && *ident.Earned >= 0
		selfGPT5 = selfValid && *ident.Earned == 0
		if selfGPT5 {
			out.Codes = append(out.Codes, "self_report_gpt5")
			match := referenceMatch
			if out.PerItemChecked {
				match = nil
				for _, group := range out.Groups {
					for _, item := range group.Items {
						if item.OrderIndex == ident.OrderIndex {
							match = group.Match
						}
					}
				}
			}
			out.SelfReport.ByPrompt = match != nil && match.Identity == "GPT-5"
		}
	}
	out.Knowledge = Knowledge{len(r.KnowledgeRows), len(in.Peers), contradictions(r.KnowledgeRows, in.Peers)}
	knowledgeOlder := len(out.Knowledge.Contradictions) > 0
	if knowledgeOlder {
		out.Codes = append(out.Codes, "knowledge_older")
	}
	out.Vision.Status = visionStatus(r.Vision)
	for _, inPeer := range in.Peers {
		peer := comparePeer(inPeer)
		if peer.Vision == "full" {
			out.Vision.PeerFull++
		}
		out.Peers = append(out.Peers, peer)
	}
	visionMismatch := out.Vision.Status == "zero" && out.Vision.PeerFull >= minKnowers
	if visionMismatch {
		out.Codes = append(out.Codes, "vision_mismatch")
	}
	anomaly := promptMismatch || selfGPT5 || visionMismatch
	switch {
	case !out.Terminal || (out.Reference == nil && len(out.Groups) == 0 && out.Knowledge.Rows == 0 && !selfValid):
		out.Grade = "E"
	case knowledgeOlder && anomaly:
		out.Grade = "D"
	case knowledgeOlder || anomaly:
		out.Grade = "C"
	case mixed:
		out.Grade = "B"
	default:
		out.Grade = "A"
	}
	raw, err := json.Marshal(out)
	out.TooLarge = err != nil || len(raw) > 256<<10
	return out
}
