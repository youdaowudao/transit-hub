package settings

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"unicode"

	xhtml "golang.org/x/net/html"
)

// CleanupRow contains only the columns explicitly covered by notification cleanup.
// Its JSON may contain stored credentials; it must never be printed or logged.
type CleanupRow struct {
	Table  string          `json:"table"`
	ID     string          `json:"id,omitempty"`
	Before json.RawMessage `json:"before"`
	After  json.RawMessage `json:"after"`
}

type CleanupSummary struct {
	ChannelsRemoved       int `json:"channelsRemoved"`
	RecipientRefsRemoved  int `json:"recipientRefsRemoved"`
	NotificationSwitches  int `json:"notificationSwitchesDisabled"`
	DefaultTemplates      int `json:"defaultTemplatesUpdated"`
	CustomLegacyTemplates int `json:"customTemplatesWithLegacyMarkers"`
	RowsChanged           int `json:"rowsChanged"`
}

type CleanupPlan struct {
	UserID         string         `json:"userId"`
	AdminAccountID string         `json:"adminAccountId"`
	Digest         string         `json:"digest"`
	Summary        CleanupSummary `json:"summary"`
	Rows           []CleanupRow   `json:"rows"`
	Snapshot       []CleanupRow   `json:"snapshot"`
}

var ErrCleanupBackupIntegrity = errors.New("backup integrity validation failed")

// PlanNotificationCleanup is pure: preview never writes a file, database, or configuration.
// Unknown JSON fields and every amount, multiplier, business flag, and timestamp are retained.
func PlanNotificationCleanup(userID, adminAccountID string, rows []CleanupRow) (CleanupPlan, error) {
	plan := CleanupPlan{UserID: userID, AdminAccountID: adminAccountID, Rows: make([]CleanupRow, 0), Snapshot: make([]CleanupRow, len(rows))}
	for i, row := range rows {
		if len(row.After) != 0 {
			return plan, errors.New("invalid cleanup snapshot")
		}
		plan.Snapshot[i] = CleanupRow{Table: row.Table, ID: row.ID, Before: append(json.RawMessage(nil), row.Before...)}
	}
	digestInput, err := json.Marshal(struct {
		UserID, AdminAccountID string
		Rows                   []CleanupRow
	}{userID, adminAccountID, plan.Snapshot})
	if err != nil {
		return plan, errors.New("invalid cleanup snapshot")
	}
	sum := sha256.Sum256(digestInput)
	plan.Digest = hex.EncodeToString(sum[:])
	channels := DefaultNotificationChannelSettings()
	for _, row := range plan.Snapshot {
		if row.Table == "notification_channel_settings" {
			if err := unmarshalNotificationChannelSettings(row.Before, &channels); err != nil {
				return plan, errors.New("invalid notification settings")
			}
		}
	}
	for _, row := range plan.Snapshot {
		var after []byte
		switch row.Table {
		case "notification_channel_settings":
			value, err := cleanupObject(row.Before)
			if err != nil {
				return plan, err
			}
			for _, key := range []string{"dingtalk", "wecom", "qq", "feishu"} {
				if raw, ok := value[key]; ok {
					var list []json.RawMessage
					if json.Unmarshal(raw, &list) == nil {
						plan.Summary.ChannelsRemoved += len(list)
					} else if !bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
						plan.Summary.ChannelsRemoved++
					}
					delete(value, key)
				}
			}
			// Preserve supported Telegram JSON, including fields added by future versions.
			// Only wrap an existing legacy object; never regenerate IDs or credentials.
			if telegram, exists := value["telegram"]; exists && len(bytes.TrimSpace(telegram)) > 0 && bytes.TrimSpace(telegram)[0] == '{' {
				value["telegram"], _ = json.Marshal([]json.RawMessage{telegram})
			}
			after, _ = json.Marshal(value)
		case "strategy_settings":
			value, err := cleanupObject(row.Before)
			if err != nil {
				return plan, err
			}
			if err := cleanupRecipientFields(value, "balanceNotifyBotIds", "enableBalanceWarning", "balanceNotifyRecipientsInvalid", channels, &plan.Summary); err != nil {
				return plan, err
			}
			if err := cleanupRecipientFields(value, "multiplierNotifyBotIds", "enableMultiplierAlert", "multiplierNotifyRecipientsInvalid", channels, &plan.Summary); err != nil {
				return plan, err
			}
			cleanupBalanceTemplate(value, &plan.Summary)
			after, _ = json.Marshal(value)
		case "group_rate_campaigns":
			value, err := cleanupObject(row.Before)
			if err != nil {
				return plan, err
			}
			if err := cleanupRecipientFields(value, "botIds", "enabled", "recipientsInvalid", channels, &plan.Summary); err != nil {
				return plan, err
			}
			countCustomTemplate(value, "startTemplate", &plan.Summary)
			countCustomTemplate(value, "endTemplate", &plan.Summary)
			after, _ = json.Marshal(value)
		case "my_site_states":
			var mappings []map[string]json.RawMessage
			if err := json.Unmarshal(row.Before, &mappings); err != nil {
				return plan, errors.New("invalid mappings")
			}
			for _, value := range mappings {
				if err := cleanupRecipientFields(value, "autoPricingNotifyBotIds", "enableAutoPricingNotify", "autoPricingNotifyRecipientsInvalid", channels, &plan.Summary); err != nil {
					return plan, err
				}
				countCustomTemplate(value, "autoPricingNotifyTemplate", &plan.Summary)
			}
			after, _ = json.Marshal(mappings)
		case "email_templates":
			var template EmailTemplate
			if err := json.Unmarshal(row.Before, &template); err != nil {
				return plan, errors.New("invalid email template")
			}
			projected := projectBuiltInEmailTemplate(template)
			if projected.HTMLBody != template.HTMLBody {
				plan.Summary.DefaultTemplates++
			} else if hasLegacyTemplateMarkers(template.HTMLBody) {
				plan.Summary.CustomLegacyTemplates++
			}
			after = row.Before
			if projected.HTMLBody != template.HTMLBody {
				value, err := cleanupObject(row.Before)
				if err != nil {
					return plan, err
				}
				value["htmlBody"], _ = json.Marshal(projected.HTMLBody)
				after, _ = json.Marshal(value)
			}
		default:
			return plan, errors.New("unsupported cleanup table")
		}
		if !sameCleanupJSON(row.Before, after) {
			row.After = after
			plan.Rows = append(plan.Rows, row)
			plan.Summary.RowsChanged++
		}
	}
	return plan, nil
}

func cleanupObject(raw []byte) (map[string]json.RawMessage, error) {
	var value map[string]json.RawMessage
	if err := json.Unmarshal(raw, &value); err != nil || value == nil {
		return nil, errors.New("invalid cleanup object")
	}
	return value, nil
}

func cleanupRecipientFields(value map[string]json.RawMessage, idsField, enabledField, warningField string, channels NotificationChannelSettings, summary *CleanupSummary) error {
	if _, exists := value[idsField]; !exists {
		return nil
	}
	var original []string
	if err := json.Unmarshal(value[idsField], &original); err != nil {
		return errors.New("invalid notification recipient list")
	}
	if !hasInvalidRecipientIDs(original, channels) {
		return nil
	}
	ids := projectRecipientIDs(original, channels)
	valid := make(map[string]bool, len(ids))
	for _, id := range ids {
		valid[id] = true
	}
	for _, id := range original {
		if !valid[id] {
			summary.RecipientRefsRemoved++
		}
	}
	value[idsField], _ = json.Marshal(ids)
	value[warningField] = json.RawMessage("true")
	if len(ids) == 0 {
		if bytes.Equal(bytes.TrimSpace(value[enabledField]), []byte("true")) {
			summary.NotificationSwitches++
		}
		value[enabledField] = json.RawMessage("false")
	}
	return nil
}

func cleanupBalanceTemplate(value map[string]json.RawMessage, summary *CleanupSummary) {
	var original string
	if json.Unmarshal(value["balanceTemplate"], &original) != nil {
		return
	}
	projected := projectDefaultBalanceTemplate(original)
	if original != projected {
		value["balanceTemplate"], _ = json.Marshal(projected)
		summary.DefaultTemplates++
	} else if hasLegacyTemplateMarkers(original) {
		summary.CustomLegacyTemplates++
	}
}

func hasLegacyTemplateMarkers(value string) bool {
	return strings.ContainsAny(value, "¥￥") || strings.Contains(value, "CNY") || strings.Contains(value, "RMB") || strings.Contains(value, "人民币") || strings.Contains(value, "zh-CN") || hasAmountUnitMarker(value)
}

var amountUnitMarker = regexp.MustCompile(`(?i)(?:\{(?:balance|threshold|amount|price|cost|revenue|reward|total)\}|[+-]?[0-9]+(?:\.[0-9]+)?)\s*元`)

func hasAmountUnitMarker(value string) bool {
	value = visibleAmountTemplateText(value)
	for _, match := range amountUnitMarker.FindAllStringIndex(value, -1) {
		suffix := value[match[1]:]
		if strings.HasPrefix(suffix, "素") || strings.HasPrefix(suffix, "组") || strings.HasPrefix(suffix, "数据") || strings.HasPrefix(suffix, "件") {
			continue
		}
		return true
	}
	return false
}

// Inspect visible text without modifying the stored template. The HTML parser
// decodes entities, and Unicode whitespace includes non-breaking spaces.
func visibleAmountTemplateText(source string) string {
	document, err := xhtml.Parse(strings.NewReader(source))
	if err != nil {
		return source
	}
	var text strings.Builder
	var visit func(*xhtml.Node)
	visit = func(node *xhtml.Node) {
		if node.Type == xhtml.ElementNode {
			switch strings.ToLower(node.Data) {
			case "script", "style", "head", "template", "noscript":
				return
			}
		}
		if node.Type == xhtml.TextNode {
			text.WriteString(node.Data)
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			visit(child)
		}
	}
	visit(document)
	return strings.Map(func(value rune) rune {
		if unicode.IsSpace(value) {
			return ' '
		}
		return value
	}, visibleMarkdownTemplateText(text.String()))
}

var amountMarkdownFormats = []*regexp.Regexp{
	regexp.MustCompile("`+([^`]+)`+"),
	regexp.MustCompile(`\*\*([^*]+)\*\*`),
	regexp.MustCompile(`__([^_]+)__`),
	regexp.MustCompile(`\*([^*]+)\*`),
	regexp.MustCompile(`_([^_]+)_`),
	regexp.MustCompile(`~~([^~]+)~~`),
}

// Markdown formatting and link destinations are inspected as displayed text.
// This is detection only: the original template is neither returned nor saved here.
func visibleMarkdownTemplateText(source string) string {
	brackets := markdownDelimiterMatches(source, '[', ']')
	destinations := markdownDelimiterMatches(source, '(', ')')
	var text strings.Builder
	for index := 0; index < len(source); {
		if source[index] == '[' {
			labelEnd, labelOK := brackets[index]
			if labelOK && labelEnd+1 < len(source) && source[labelEnd+1] == '(' {
				destinationEnd, destinationOK := destinations[labelEnd+1]
				if destinationOK {
					text.WriteString(source[index+1 : labelEnd])
					index = destinationEnd + 1
					continue
				}
			}
		}
		text.WriteByte(source[index])
		index++
	}
	value := text.String()
	for _, format := range amountMarkdownFormats {
		value = format.ReplaceAllString(value, "$1")
	}
	return value
}

// Pair delimiters once so deeply nested or unmatched labels remain linear to inspect.
func markdownDelimiterMatches(source string, open, close byte) map[int]int {
	matches := make(map[int]int)
	stack := make([]int, 0)
	for index := 0; index < len(source); index++ {
		if source[index] == '\\' {
			index++
			continue
		}
		if source[index] == open {
			stack = append(stack, index)
		} else if source[index] == close && len(stack) > 0 {
			start := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			matches[start] = index
		}
	}
	return matches
}

// ValidateNotificationCleanupBackup binds the complete original snapshot to the
// separately reviewed digest, then recomputes the only permitted changes.
// It must run before creating any database connection for restore.
func ValidateNotificationCleanupBackup(backup CleanupPlan, userID, adminAccountID, expectedDigest string) error {
	invalid := ErrCleanupBackupIntegrity
	if backup.UserID != userID || backup.AdminAccountID != adminAccountID || backup.Digest != expectedDigest || backup.Snapshot == nil {
		return invalid
	}
	canonical, err := PlanNotificationCleanup(userID, adminAccountID, backup.Snapshot)
	if err != nil || canonical.Digest != expectedDigest || canonical.Summary != backup.Summary || len(canonical.Rows) != len(backup.Rows) {
		return invalid
	}
	for i, row := range canonical.Rows {
		candidate := backup.Rows[i]
		if row.Table != candidate.Table || row.ID != candidate.ID || !sameCleanupJSON(row.Before, candidate.Before) || !sameCleanupJSON(row.After, candidate.After) {
			return invalid
		}
	}
	return nil
}

func sameCleanupJSON(left, right []byte) bool {
	var a, b any
	decode := func(raw []byte, value *any) error {
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.UseNumber()
		return decoder.Decode(value)
	}
	if decode(left, &a) != nil || decode(right, &b) != nil {
		return false
	}
	aJSON, _ := json.Marshal(a)
	bJSON, _ := json.Marshal(b)
	return bytes.Equal(aJSON, bJSON)
}

// SameCleanupJSON is used by the approved rollback command to detect concurrent edits.
func SameCleanupJSON(left, right []byte) bool { return sameCleanupJSON(left, right) }

func countCustomTemplate(value map[string]json.RawMessage, key string, summary *CleanupSummary) {
	var template string
	if json.Unmarshal(value[key], &template) == nil && hasLegacyTemplateMarkers(template) {
		summary.CustomLegacyTemplates++
	}
}
