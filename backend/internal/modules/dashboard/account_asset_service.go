package dashboard

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"transithub/backend/internal/modules/my_sites"
	"transithub/backend/internal/shared/businesstime"
)

var errInvalidAllocationCount = errors.New("dashboard.accountAsset.errors.invalidCount")

var errInvalidAccountBatch = errors.New("dashboard.accountAsset.errors.invalidBatch")

var ErrAccountAssetNotFound = errors.New("dashboard.accountAsset.errors.notFound")

type AccountAssetRepository interface {
	CreateAccountBatch(ctx context.Context, batch AccountBatch, assets []AccountAsset, links []AccountLink, costs []AdditionalCostRecord) (AccountBatchResult, error)
	ListAccountAssets(ctx context.Context, userID, adminAccountID string, filter AccountAssetFilter) (AccountAssetPage, error)
	GetAccountAsset(ctx context.Context, userID, adminAccountID, assetID string) (AccountAsset, error)
	AppendAccountEvent(ctx context.Context, event AccountEvent) (AccountEventResult, error)
	ReplaceAccountLink(ctx context.Context, event AccountEvent, link *AccountLink, upstreamReferenceURL string) error
	GetAccountAssetDetail(ctx context.Context, userID, adminAccountID, assetID string) (AccountAssetDetail, error)
	ListAccountCostLedger(ctx context.Context, userID, adminAccountID string, filter AccountCostLedgerFilter) (AccountCostLedgerPage, error)
}

func (s *AccountAssetService) ListCostLedger(ctx context.Context, userID string, filter AccountCostLedgerFilter) (AccountCostLedgerPage, error) {
	if s == nil || s.repository == nil || s.accounts == nil {
		return AccountCostLedgerPage{}, errInvalidAccountBatch
	}
	adminAccountID, err := s.accounts.RequireCurrentID(ctx, userID)
	if err != nil {
		return AccountCostLedgerPage{}, err
	}
	return s.repository.ListAccountCostLedger(ctx, userID, adminAccountID, filter)
}

func (s *AccountAssetService) ListAssets(ctx context.Context, userID string, filter AccountAssetFilter) (AccountAssetPage, error) {
	if s == nil || s.repository == nil || s.accounts == nil {
		return AccountAssetPage{}, errInvalidAccountBatch
	}
	adminAccountID, err := s.accounts.RequireCurrentID(ctx, userID)
	if err != nil {
		return AccountAssetPage{}, err
	}
	return s.repository.ListAccountAssets(ctx, userID, adminAccountID, filter)
}

func (s *AccountAssetService) GetAssetDetail(ctx context.Context, userID, assetID string) (AccountAssetDetail, error) {
	if s == nil || s.repository == nil || s.accounts == nil || strings.TrimSpace(assetID) == "" {
		return AccountAssetDetail{}, errInvalidAccountBatch
	}
	adminAccountID, err := s.accounts.RequireCurrentID(ctx, userID)
	if err != nil {
		return AccountAssetDetail{}, err
	}
	return s.repository.GetAccountAssetDetail(ctx, userID, adminAccountID, assetID)
}

func (s *AccountAssetService) AppendEvent(ctx context.Context, userID, assetID, idempotencyKey string, event AccountEvent) (AccountEventResult, error) {
	if s == nil || s.repository == nil || s.accounts == nil || strings.TrimSpace(assetID) == "" || strings.TrimSpace(idempotencyKey) == "" {
		return AccountEventResult{}, errInvalidAccountBatch
	}
	adminAccountID, err := s.accounts.RequireCurrentID(ctx, userID)
	if err != nil {
		return AccountEventResult{}, err
	}
	event.ID = s.newID()
	event.UserID = userID
	event.AdminAccountID = adminAccountID
	event.AccountAssetID = strings.TrimSpace(assetID)
	event.IdempotencyKey = strings.TrimSpace(idempotencyKey)
	event.Note = strings.TrimSpace(event.Note)
	event.Identifier = strings.TrimSpace(event.Identifier)
	event.Platform = strings.TrimSpace(event.Platform)
	event.Channel = strings.TrimSpace(event.Channel)
	event.AccountType = strings.TrimSpace(event.AccountType)
	if event.UpstreamReferenceURL != nil {
		value := strings.TrimSpace(*event.UpstreamReferenceURL)
		event.UpstreamReferenceURL = &value
	}
	if event.EventType == AccountEventMetadataCorrection && event.UpstreamReferenceURL != nil {
		if err := validateAccountReferenceURL(*event.UpstreamReferenceURL); err != nil {
			return AccountEventResult{}, err
		}
	}
	event.CreatedAt = s.now().UTC()
	return s.repository.AppendAccountEvent(ctx, event)
}

func (s *AccountAssetService) ReplaceLink(ctx context.Context, userID, assetID, idempotencyKey string, input AccountLinkInput) (AccountAssetDetail, error) {
	if s == nil || s.repository == nil || s.accounts == nil || strings.TrimSpace(assetID) == "" || strings.TrimSpace(idempotencyKey) == "" {
		return AccountAssetDetail{}, errInvalidAccountBatch
	}
	adminAccountID, err := s.accounts.RequireCurrentID(ctx, userID)
	if err != nil {
		return AccountAssetDetail{}, err
	}
	input.ConnectionID = strings.TrimSpace(input.ConnectionID)
	input.UpstreamReferenceURL = strings.TrimSpace(input.UpstreamReferenceURL)
	input.EffectiveFrom = strings.TrimSpace(input.EffectiveFrom)
	input.Note = strings.TrimSpace(input.Note)
	if err := validateAccountReferenceURL(input.UpstreamReferenceURL); err != nil {
		return AccountAssetDetail{}, err
	}
	if input.EffectiveFrom == "" {
		effective := s.now().In(businesstime.Location())
		if input.ConnectionID != "" {
			effective = effective.AddDate(0, 0, 1)
		}
		input.EffectiveFrom = effective.Format("2006-01-02")
	}
	if _, err := time.ParseInLocation("2006-01-02", input.EffectiveFrom, businesstime.Location()); err != nil {
		return AccountAssetDetail{}, errInvalidAccountBatch
	}
	today := s.now().In(businesstime.Location()).Format("2006-01-02")
	if input.ConnectionID != "" && input.EffectiveFrom == today {
		if !input.ManualSameDaySplit || input.PreviousQuotaUsedMicros == nil || input.PreviousRevenueCents == nil ||
			input.ReplacementQuotaUsedMicros == nil || input.ReplacementRevenueCents == nil ||
			*input.PreviousQuotaUsedMicros < 0 || *input.PreviousRevenueCents < 0 ||
			*input.ReplacementQuotaUsedMicros < 0 || *input.ReplacementRevenueCents < 0 {
			return AccountAssetDetail{}, errInvalidAccountBatch
		}
	} else if input.ManualSameDaySplit {
		return AccountAssetDetail{}, errInvalidAccountBatch
	}

	event := AccountEvent{
		ID: s.newID(), UserID: userID, AdminAccountID: adminAccountID, AccountAssetID: strings.TrimSpace(assetID),
		EventType: AccountEventLinkChange, EffectiveDate: input.EffectiveFrom, Note: input.Note,
		IdempotencyKey: strings.TrimSpace(idempotencyKey), CreatedAt: s.now().UTC(),
	}
	var link *AccountLink
	if input.ConnectionID != "" {
		if s.connections == nil {
			return AccountAssetDetail{}, errInvalidAccountBatch
		}
		connections, err := s.connections.ListRealConnectionsForWorkspace(ctx, userID, adminAccountID)
		if err != nil {
			return AccountAssetDetail{}, err
		}
		var selected *my_sites.RealConnection
		for _, connection := range uniqueDirectProfitConnections(connections) {
			if connection.ID == input.ConnectionID {
				copy := connection
				selected = &copy
				break
			}
		}
		if selected == nil {
			return AccountAssetDetail{}, errInvalidAccountBatch
		}
		ownGroupName := ""
		if len(selected.OwnGroupNames) == 1 {
			ownGroupName = selected.OwnGroupNames[0]
		}
		link = &AccountLink{
			ID: s.newID(), UserID: userID, AdminAccountID: adminAccountID, AccountAssetID: event.AccountAssetID,
			ConnectionID: selected.ID, UpstreamSiteID: selected.UpstreamSiteID, UpstreamKeyID: selected.UpstreamKeyID,
			ScopeAdminAccountID: selected.AdminAccountID, OwnGroupID: selected.OwnGroupIDs[0],
			ConnectionName: selected.UpstreamGroupName, SiteName: selected.UpstreamSiteID,
			KeyName: selected.UpstreamKeyID, OwnGroupName: ownGroupName,
			UpstreamReferenceURL: input.UpstreamReferenceURL, EffectiveFrom: input.EffectiveFrom,
			ManualSameDaySplit: input.ManualSameDaySplit, CreatedAt: event.CreatedAt,
			PreviousQuotaUsedMicros: input.PreviousQuotaUsedMicros, PreviousRevenueCents: input.PreviousRevenueCents,
			ReplacementQuotaUsedMicros: input.ReplacementQuotaUsedMicros, ReplacementRevenueCents: input.ReplacementRevenueCents,
		}
	}
	if err := s.repository.ReplaceAccountLink(ctx, event, link, input.UpstreamReferenceURL); err != nil {
		return AccountAssetDetail{}, err
	}
	return s.repository.GetAccountAssetDetail(ctx, userID, adminAccountID, event.AccountAssetID)
}

type AccountAssetService struct {
	repository  AccountAssetRepository
	accounts    AdminAccountService
	connections RealConnectionReader
	newID       func() string
	now         func() time.Time
}

func NewAccountAssetService(repository AccountAssetRepository, accounts AdminAccountService, connections RealConnectionReader) *AccountAssetService {
	return &AccountAssetService{
		repository: repository, accounts: accounts, connections: connections,
		newID: mustMetricsID, now: time.Now,
	}
}

func allocateCents(total int64, count int) ([]int64, error) {
	if count <= 0 {
		return nil, errInvalidAllocationCount
	}
	base := total / int64(count)
	result := make([]int64, count)
	for index := range result {
		result[index] = base
	}
	result[count-1] += total - base*int64(count)
	return result, nil
}

func (s *AccountAssetService) CreateBatch(ctx context.Context, userID string, input AccountBatchInput) (AccountBatchResult, error) {
	if s == nil || s.repository == nil || s.accounts == nil {
		return AccountBatchResult{}, errInvalidAccountBatch
	}
	adminAccountID, err := s.accounts.RequireCurrentID(ctx, userID)
	if err != nil {
		return AccountBatchResult{}, err
	}
	input.BatchName = strings.TrimSpace(input.BatchName)
	input.Platform = strings.TrimSpace(input.Platform)
	input.Channel = strings.TrimSpace(input.Channel)
	input.AccountType = strings.TrimSpace(input.AccountType)
	input.PurchaseDate = strings.TrimSpace(input.PurchaseDate)
	input.RecognitionStartDate = strings.TrimSpace(input.RecognitionStartDate)
	input.PurchaseURL = strings.TrimSpace(input.PurchaseURL)
	input.DefaultUpstreamReferenceURL = strings.TrimSpace(input.DefaultUpstreamReferenceURL)
	input.Note = strings.TrimSpace(input.Note)
	input.IdempotencyKey = strings.TrimSpace(input.IdempotencyKey)
	if input.BatchName == "" {
		input.BatchName = input.Platform + " " + input.PurchaseDate
	}
	if input.IdempotencyKey == "" || input.Platform == "" || input.Channel == "" || input.AccountType == "" || input.Quantity <= 0 || input.Quantity > 500 || input.TotalAmountCents < 0 || len(input.Identifiers) > input.Quantity || len(input.Accounts) > input.Quantity {
		return AccountBatchResult{}, errInvalidAccountBatch
	}
	if err := validateAccountReferenceURL(input.PurchaseURL); err != nil {
		return AccountBatchResult{}, err
	}
	if err := validateAccountReferenceURL(input.DefaultUpstreamReferenceURL); err != nil {
		return AccountBatchResult{}, err
	}
	purchaseDate, err := time.ParseInLocation("2006-01-02", input.PurchaseDate, businesstime.Location())
	if err != nil {
		return AccountBatchResult{}, errInvalidAccountBatch
	}
	if input.RecognitionStartDate == "" {
		input.RecognitionStartDate = input.PurchaseDate
	}
	recognitionStartDate, err := time.ParseInLocation("2006-01-02", input.RecognitionStartDate, businesstime.Location())
	if err != nil || recognitionStartDate.Before(purchaseDate) {
		return AccountBatchResult{}, errInvalidAccountBatch
	}
	if input.AccountingMode != AccountingModeReplace && input.AccountingMode != AccountingModeAdditive {
		return AccountBatchResult{}, errInvalidAccountBatch
	}
	if input.RecognitionMode != RecognitionModeImmediate && input.RecognitionMode != RecognitionModeDaily && input.RecognitionMode != RecognitionModeQuota {
		return AccountBatchResult{}, errInvalidAccountBatch
	}
	if input.RecognitionMode == RecognitionModeDaily && input.RecognitionDays <= 0 {
		return AccountBatchResult{}, errInvalidAccountBatch
	}
	if input.StatsMode != StatsModeAutomatic && input.StatsMode != StatsModeManual {
		return AccountBatchResult{}, errInvalidAccountBatch
	}

	amounts, err := allocateCents(input.TotalAmountCents, input.Quantity)
	if err != nil {
		return AccountBatchResult{}, err
	}
	now := s.now().UTC()
	batch := AccountBatch{
		ID: s.newID(), UserID: userID, AdminAccountID: adminAccountID, IdempotencyKey: input.IdempotencyKey,
		BatchName: input.BatchName, Platform: input.Platform, Channel: input.Channel, AccountType: input.AccountType,
		PurchaseDate: input.PurchaseDate, PurchaseURL: input.PurchaseURL, DefaultUpstreamReferenceURL: input.DefaultUpstreamReferenceURL,
		Quantity: input.Quantity, TotalAmountCents: input.TotalAmountCents, AccountingMode: input.AccountingMode,
		RecognitionMode: input.RecognitionMode, RecognitionStartDate: input.RecognitionStartDate,
		RecognitionDays: input.RecognitionDays, StatsMode: input.StatsMode, Note: input.Note, CreatedAt: now,
	}
	assets := make([]AccountAsset, 0, input.Quantity)
	links := make([]AccountLink, 0, input.Quantity)
	costs := make([]AdditionalCostRecord, 0, input.Quantity*max(input.RecognitionDays, 1))
	requestedConnections := make(map[string]struct{})
	for _, override := range input.Accounts {
		connectionID := strings.TrimSpace(override.ConnectionID)
		if connectionID == "" {
			continue
		}
		if _, exists := requestedConnections[connectionID]; exists {
			return AccountBatchResult{}, errInvalidAccountBatch
		}
		requestedConnections[connectionID] = struct{}{}
	}
	eligibleConnections := make(map[string]my_sites.RealConnection)
	if len(requestedConnections) > 0 {
		if s.connections == nil {
			return AccountBatchResult{}, errInvalidAccountBatch
		}
		connections, err := s.connections.ListRealConnectionsForWorkspace(ctx, userID, adminAccountID)
		if err != nil {
			return AccountBatchResult{}, err
		}
		for _, connection := range uniqueDirectProfitConnections(connections) {
			eligibleConnections[connection.ID] = connection
		}
	}
	for index, amount := range amounts {
		identifier := ""
		var override AccountAssetInput
		if index < len(input.Accounts) {
			override = input.Accounts[index]
			identifier = strings.TrimSpace(override.Identifier)
		}
		if index < len(input.Identifiers) {
			if identifier == "" {
				identifier = strings.TrimSpace(input.Identifiers[index])
			}
		}
		if identifier == "" {
			identifier = fmt.Sprintf("%s-%03d", input.BatchName, index+1)
		}
		upstreamReferenceURL := strings.TrimSpace(override.UpstreamReferenceURL)
		if upstreamReferenceURL == "" {
			upstreamReferenceURL = input.DefaultUpstreamReferenceURL
		}
		if err := validateAccountReferenceURL(upstreamReferenceURL); err != nil {
			return AccountBatchResult{}, err
		}
		connectionID := strings.TrimSpace(override.ConnectionID)
		if input.StatsMode == StatsModeAutomatic && connectionID == "" {
			return AccountBatchResult{}, errInvalidAccountBatch
		}
		if input.RecognitionMode == RecognitionModeQuota && override.QuotaTotalMicros == nil {
			return AccountBatchResult{}, errInvalidAccountBatch
		}
		if override.QuotaTotalMicros != nil && *override.QuotaTotalMicros <= 0 {
			return AccountBatchResult{}, errInvalidAccountBatch
		}
		asset := AccountAsset{
			ID: s.newID(), UserID: userID, AdminAccountID: adminAccountID, BatchID: batch.ID,
			Identifier: identifier, Platform: input.Platform, Channel: input.Channel, AccountType: input.AccountType,
			PurchaseCostCents: amount, AccountingMode: input.AccountingMode, RecognitionMode: input.RecognitionMode,
			RecognitionStartDate: input.RecognitionStartDate, RecognitionDays: input.RecognitionDays,
			StatsMode: input.StatsMode, CurrentStatus: AccountStatusUnactivated, QuotaTotalMicros: override.QuotaTotalMicros,
			UpstreamReferenceURL: upstreamReferenceURL, CreatedAt: now, UpdatedAt: now,
		}
		assets = append(assets, asset)
		if connectionID != "" {
			connection, exists := eligibleConnections[connectionID]
			if !exists {
				return AccountBatchResult{}, errInvalidAccountBatch
			}
			effectiveFrom := strings.TrimSpace(override.LinkEffectiveFrom)
			if effectiveFrom == "" {
				effectiveFrom = purchaseDate.AddDate(0, 0, 1).Format("2006-01-02")
			}
			parsedEffectiveFrom, parseErr := time.ParseInLocation("2006-01-02", effectiveFrom, businesstime.Location())
			if parseErr != nil || parsedEffectiveFrom.Before(purchaseDate) || effectiveFrom == input.PurchaseDate && !override.ManualSameDaySplit {
				return AccountBatchResult{}, errInvalidAccountBatch
			}
			ownGroupName := ""
			if len(connection.OwnGroupNames) == 1 {
				ownGroupName = connection.OwnGroupNames[0]
			}
			links = append(links, AccountLink{
				ID: s.newID(), UserID: userID, AdminAccountID: adminAccountID, AccountAssetID: asset.ID,
				ConnectionID: connection.ID, UpstreamSiteID: connection.UpstreamSiteID, UpstreamKeyID: connection.UpstreamKeyID,
				ScopeAdminAccountID: connection.AdminAccountID, OwnGroupID: connection.OwnGroupIDs[0],
				ConnectionName: connection.UpstreamGroupName, SiteName: connection.UpstreamSiteID,
				KeyName: connection.UpstreamKeyID, OwnGroupName: ownGroupName,
				UpstreamReferenceURL: upstreamReferenceURL, EffectiveFrom: effectiveFrom,
				ManualSameDaySplit: override.ManualSameDaySplit, CreatedAt: now,
			})
		}
		var schedule []DatedAmount
		switch input.RecognitionMode {
		case RecognitionModeImmediate:
			schedule = []DatedAmount{{BusinessDate: input.RecognitionStartDate, AmountCents: amount}}
		case RecognitionModeDaily:
			schedule, err = allocateDailyCost(amount, input.RecognitionStartDate, input.RecognitionDays)
			if err != nil {
				return AccountBatchResult{}, err
			}
		}
		for scheduleIndex, item := range schedule {
			costs = append(costs, AdditionalCostRecord{
				ID: s.newID(), UserID: userID, AdminAccountID: adminAccountID,
				Type: AdditionalCostAccountPurchase, Name: identifier, BusinessDate: item.BusinessDate,
				AmountCents: item.AmountCents, Amount: float64(item.AmountCents) / 100,
				OriginalAmount: float64(amount) / 100, Days: input.RecognitionDays,
				SourceID: asset.ID, BatchID: batch.ID, AccountAssetID: asset.ID,
				Note: fmt.Sprintf("%s %d/%d", input.RecognitionMode, scheduleIndex+1, len(schedule)), CreatedAt: now,
			})
		}
	}
	return s.repository.CreateAccountBatch(ctx, batch, assets, links, costs)
}

func validateAccountReferenceURL(value string) error {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	parsed, err := url.Parse(value)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil {
		return errInvalidAccountBatch
	}
	for key := range parsed.Query() {
		normalized := strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(strings.TrimSpace(key), "-", "_"), " ", "_"))
		switch normalized {
		case "token", "access_token", "refresh_token", "password", "passwd", "cookie", "authorization", "auth", "key", "api_key", "apikey":
			return errInvalidAccountBatch
		}
	}
	return nil
}
