package httpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"transithub/backend/internal/modules/settings"
	"transithub/backend/internal/modules/upstream"
)

type cleanupNotificationTransport struct{ messages []string }

func (transport *cleanupNotificationTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	var payload struct {
		Text string `json:"text"`
	}
	if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
		return nil, err
	}
	transport.messages = append(transport.messages, payload.Text)
	return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"ok":true}`))}, nil
}

func TestCleanupNotificationActualRiseFallAndNoDisappearedEventPostgres(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_DATABASE_URL"))
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is required for explicit PostgreSQL verification")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal("cannot connect to PostgreSQL test source")
	}
	defer admin.Close()
	schema := fmt.Sprintf("group_cleanup_notification_%d", time.Now().UnixNano())
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		if _, err := admin.Exec(cleanupCtx, "DROP SCHEMA "+quoted+" CASCADE"); err != nil {
			t.Error(err)
		}
	}()
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal("cannot parse PostgreSQL test source")
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal("cannot connect to isolated PostgreSQL test schema")
	}
	defer pool.Close()
	repository := settings.NewRepository(pool)
	if err := repository.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	if err := repository.SaveNotificationChannels(ctx, "fixture-user", "fixture-workspace", settings.NotificationChannelSettings{
		Telegram: []settings.TelegramChannelSettings{{ID: "fixture-bot", Enabled: true, BotToken: "fake-test-bot", ChatID: "fake-chat"}},
	}); err != nil {
		t.Fatal(err)
	}
	transport := &cleanupNotificationTransport{}
	service := settings.NewService(&http.Client{Transport: transport}, repository)
	strategy := settings.StrategySettings{EnableMultiplierAlert: true, MultiplierNotifyBotIDs: []string{"fixture-bot"}}
	initial := 2.0
	old := upstream.Metrics{Groups: []upstream.GroupInfo{{ID: "g", Name: "same", Multiplier: &initial}}}
	for _, scenario := range []struct {
		name     string
		groups   []upstream.GroupInfo
		want     int
		fragment string
	}{
		{name: "disappeared", groups: []upstream.GroupInfo{}, want: 0},
		{name: "auto", groups: []upstream.GroupInfo{{ID: "g", Name: "same", MultiplierMode: "auto"}}, want: 0},
		{name: "replacement-ID", groups: []upstream.GroupInfo{{ID: "new-g", Name: "same", Multiplier: &initial}}, want: 0},
		{name: "unchanged", groups: old.Groups, want: 0},
		{name: "rise", groups: []upstream.GroupInfo{{ID: "g", Name: "same", Multiplier: cleanupNotificationRate(3)}}, want: 1, fragment: "上升"},
		{name: "fall", groups: []upstream.GroupInfo{{ID: "g", Name: "same", Multiplier: cleanupNotificationRate(1)}}, want: 1, fragment: "下降"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			transport.messages = nil
			checkMultiplierChanges(ctx, service, notificationEligibilityFunc(func(context.Context, string, string, string) (bool, error) { return true, nil }), strategy, "fixture-user", "fixture-workspace", "fixture-site", "fixture", old, upstream.Metrics{Groups: scenario.groups})
			if len(transport.messages) != scenario.want {
				t.Fatalf("actual notification requests=%d want=%d", len(transport.messages), scenario.want)
			}
			if scenario.want > 0 && !strings.Contains(transport.messages[0], scenario.fragment) {
				t.Fatal("notification lost rise/fall meaning")
			}
		})
	}
}

func cleanupNotificationRate(value float64) *float64 { return &value }
