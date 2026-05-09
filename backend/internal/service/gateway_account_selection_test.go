//go:build unit

package service

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// --- helpers ---

func testTimePtr(t time.Time) *time.Time { return &t }

func makeAccWithLoad(id int64, priority int, loadRate int, lastUsed *time.Time, accType string) accountWithLoad {
	return accountWithLoad{
		account: &Account{
			ID:          id,
			Priority:    priority,
			LastUsedAt:  lastUsed,
			Type:        accType,
			Schedulable: true,
			Status:      StatusActive,
		},
		loadInfo: &AccountLoadInfo{
			AccountID:          id,
			CurrentConcurrency: 0,
			LoadRate:           loadRate,
		},
	}
}

// --- sortAccountsByPriorityAndLastUsed ---

func TestSortAccountsByPriorityAndLastUsed_ByPriority(t *testing.T) {
	now := time.Now()
	accounts := []*Account{
		{ID: 1, Priority: 5, LastUsedAt: testTimePtr(now)},
		{ID: 2, Priority: 1, LastUsedAt: testTimePtr(now)},
		{ID: 3, Priority: 3, LastUsedAt: testTimePtr(now)},
	}
	sortAccountsByPriorityAndLastUsed(accounts, false)
	require.Equal(t, int64(2), accounts[0].ID, "优先级最低的排第一")
	require.Equal(t, int64(3), accounts[1].ID)
	require.Equal(t, int64(1), accounts[2].ID)
}

func TestSortAccountsByPriorityAndLastUsed_SamePriorityByQuotaReset(t *testing.T) {
	now := time.Now().UTC()
	later := now.Add(6 * time.Hour).Format(time.RFC3339)
	sooner := now.Add(30 * time.Minute).Format(time.RFC3339)
	accounts := []*Account{
		{ID: 1, Priority: 1, LastUsedAt: nil, Extra: map[string]any{"quota_daily_reset_at": later}},
		{ID: 2, Priority: 1, LastUsedAt: nil, Extra: map[string]any{"quota_daily_reset_at": sooner}},
		{ID: 3, Priority: 1, LastUsedAt: nil},
	}

	sortAccountsByPriorityAndLastUsed(accounts, false)

	require.Equal(t, int64(2), accounts[0].ID, "更快重置的账号应排在同优先级前面")
	require.Equal(t, int64(1), accounts[1].ID)
	require.Equal(t, int64(3), accounts[2].ID, "没有重置时间的账号应排在有重置时间后面")
}

func TestSortAccountsByPriorityAndLastUsed_PrefersCodex7dReset(t *testing.T) {
	now := time.Now().UTC()
	accounts := []*Account{
		{ID: 1, Priority: 1, LastUsedAt: nil, Extra: map[string]any{"quota_weekly_reset_at": now.Add(2 * time.Hour).Format(time.RFC3339)}},
		{ID: 2, Priority: 1, LastUsedAt: nil, Extra: map[string]any{"codex_7d_reset_at": now.Add(time.Hour).Format(time.RFC3339)}},
	}

	sortAccountsByPriorityAndLastUsed(accounts, false)

	require.Equal(t, int64(2), accounts[0].ID, "codex_7d_reset_at 更早时应优先参与调度")
}

func TestSortAccountsByPriorityAndLastUsed_PriorityBeatsQuotaReset(t *testing.T) {
	now := time.Now().UTC()
	accounts := []*Account{
		{ID: 1, Priority: 5, LastUsedAt: nil, Extra: map[string]any{"quota_daily_reset_at": now.Add(-time.Hour).Format(time.RFC3339)}},
		{ID: 2, Priority: 1, LastUsedAt: nil, Extra: map[string]any{"quota_daily_reset_at": now.Add(6 * time.Hour).Format(time.RFC3339)}},
	}

	sortAccountsByPriorityAndLastUsed(accounts, false)

	require.Equal(t, int64(2), accounts[0].ID, "账号 priority 仍应优先于额度重置时间")
	require.Equal(t, int64(1), accounts[1].ID)
}

func TestSelectByLRU_SamePriorityAndLoadUsesQuotaResetBeforeLastUsed(t *testing.T) {
	now := time.Now().UTC()
	oldUsed := now.Add(-24 * time.Hour)
	recentUsed := now.Add(-time.Hour)
	accounts := []accountWithLoad{
		makeAccWithLoad(1, 1, 10, testTimePtr(oldUsed), AccountTypeAPIKey),
		makeAccWithLoad(2, 1, 10, testTimePtr(recentUsed), AccountTypeAPIKey),
	}
	accounts[0].account.Extra = map[string]any{"quota_daily_reset_at": now.Add(6 * time.Hour).Format(time.RFC3339)}
	accounts[1].account.Extra = map[string]any{"quota_daily_reset_at": now.Add(30 * time.Minute).Format(time.RFC3339)}

	result := selectByLRU(accounts, false)

	require.NotNil(t, result)
	require.Equal(t, int64(2), result.account.ID, "更快重置应优先于 LastUsedAt")
}

func TestSelectByLRU_ExpiredQuotaResetSortsFirst(t *testing.T) {
	now := time.Now().UTC()
	accounts := []accountWithLoad{
		makeAccWithLoad(1, 1, 10, nil, AccountTypeAPIKey),
		makeAccWithLoad(2, 1, 10, nil, AccountTypeAPIKey),
	}
	accounts[0].account.Extra = map[string]any{"quota_daily_reset_at": now.Add(30 * time.Minute).Format(time.RFC3339)}
	accounts[1].account.Extra = map[string]any{"quota_daily_reset_at": now.Add(-time.Minute).Format(time.RFC3339)}

	result := selectByLRU(accounts, false)

	require.NotNil(t, result)
	require.Equal(t, int64(2), result.account.ID, "已过期但未刷新的重置时间应最优先")
}

func TestSelectByLRU_UsesCodex7dResetBeforeLastUsed(t *testing.T) {
	now := time.Now().UTC()
	oldUsed := now.Add(-24 * time.Hour)
	recentUsed := now.Add(-time.Hour)
	accounts := []accountWithLoad{
		makeAccWithLoad(1, 1, 10, testTimePtr(oldUsed), AccountTypeOAuth),
		makeAccWithLoad(2, 1, 10, testTimePtr(recentUsed), AccountTypeOAuth),
	}
	accounts[0].account.Extra = map[string]any{"codex_7d_reset_at": now.Add(6 * time.Hour).Format(time.RFC3339)}
	accounts[1].account.Extra = map[string]any{"codex_7d_reset_at": now.Add(30 * time.Minute).Format(time.RFC3339)}

	result := selectByLRU(accounts, false)

	require.NotNil(t, result)
	require.Equal(t, int64(2), result.account.ID, "codex 7d 更快重置应优先于 LastUsedAt")
}

func TestSortAccountsByPriorityAndLastUsed_SamePriorityByLastUsed(t *testing.T) {
	now := time.Now()
	accounts := []*Account{
		{ID: 1, Priority: 1, LastUsedAt: testTimePtr(now)},
		{ID: 2, Priority: 1, LastUsedAt: testTimePtr(now.Add(-1 * time.Hour))},
		{ID: 3, Priority: 1, LastUsedAt: nil},
	}
	sortAccountsByPriorityAndLastUsed(accounts, false)
	require.Equal(t, int64(3), accounts[0].ID, "nil LastUsedAt 排最前")
	require.Equal(t, int64(2), accounts[1].ID, "更早使用的排前面")
	require.Equal(t, int64(1), accounts[2].ID)
}

func TestSortAccountsByPriorityAndLastUsed_PreferOAuth(t *testing.T) {
	accounts := []*Account{
		{ID: 1, Priority: 1, LastUsedAt: nil, Type: AccountTypeAPIKey},
		{ID: 2, Priority: 1, LastUsedAt: nil, Type: AccountTypeOAuth},
	}
	sortAccountsByPriorityAndLastUsed(accounts, true)
	require.Equal(t, int64(2), accounts[0].ID, "preferOAuth 时 OAuth 账号排前面")
}

func TestSortAccountsByPriorityAndLastUsed_StableSort(t *testing.T) {
	accounts := []*Account{
		{ID: 1, Priority: 1, LastUsedAt: nil, Type: AccountTypeAPIKey},
		{ID: 2, Priority: 1, LastUsedAt: nil, Type: AccountTypeAPIKey},
		{ID: 3, Priority: 1, LastUsedAt: nil, Type: AccountTypeAPIKey},
	}

	// sortAccountsByPriorityAndLastUsed 内部会在同组(Priority+LastUsedAt)内做随机打散，
	// 因此这里不再断言“稳定排序”。我们只验证：
	// 1) 元素集合不变；2) 多次运行能产生不同的顺序。
	seenFirst := map[int64]bool{}
	for i := 0; i < 100; i++ {
		cpy := make([]*Account, len(accounts))
		copy(cpy, accounts)
		sortAccountsByPriorityAndLastUsed(cpy, false)
		seenFirst[cpy[0].ID] = true

		ids := map[int64]bool{}
		for _, a := range cpy {
			ids[a.ID] = true
		}
		require.True(t, ids[1] && ids[2] && ids[3])
	}
	require.GreaterOrEqual(t, len(seenFirst), 2, "同组账号应能被随机打散")
}

func TestSortAccountsByPriorityAndLastUsed_MixedPriorityAndTime(t *testing.T) {
	now := time.Now()
	accounts := []*Account{
		{ID: 1, Priority: 2, LastUsedAt: nil},
		{ID: 2, Priority: 1, LastUsedAt: testTimePtr(now)},
		{ID: 3, Priority: 1, LastUsedAt: testTimePtr(now.Add(-1 * time.Hour))},
		{ID: 4, Priority: 2, LastUsedAt: testTimePtr(now.Add(-2 * time.Hour))},
	}
	sortAccountsByPriorityAndLastUsed(accounts, false)
	// 优先级1排前：nil < earlier
	require.Equal(t, int64(3), accounts[0].ID, "优先级1 + 更早")
	require.Equal(t, int64(2), accounts[1].ID, "优先级1 + 现在")
	// 优先级2排后：nil < time
	require.Equal(t, int64(1), accounts[2].ID, "优先级2 + nil")
	require.Equal(t, int64(4), accounts[3].ID, "优先级2 + 有时间")
}

// --- filterByMinPriority ---

func TestFilterByMinPriority_Empty(t *testing.T) {
	result := filterByMinPriority(nil)
	require.Nil(t, result)
}

func TestFilterByMinPriority_SelectsMinPriority(t *testing.T) {
	accounts := []accountWithLoad{
		makeAccWithLoad(1, 5, 10, nil, AccountTypeAPIKey),
		makeAccWithLoad(2, 1, 10, nil, AccountTypeAPIKey),
		makeAccWithLoad(3, 1, 20, nil, AccountTypeAPIKey),
		makeAccWithLoad(4, 2, 10, nil, AccountTypeAPIKey),
	}
	result := filterByMinPriority(accounts)
	require.Len(t, result, 2)
	require.Equal(t, int64(2), result[0].account.ID)
	require.Equal(t, int64(3), result[1].account.ID)
}

// --- filterByMinLoadRate ---

func TestFilterByMinLoadRate_Empty(t *testing.T) {
	result := filterByMinLoadRate(nil)
	require.Nil(t, result)
}

func TestFilterByMinLoadRate_SelectsMinLoadRate(t *testing.T) {
	accounts := []accountWithLoad{
		makeAccWithLoad(1, 1, 30, nil, AccountTypeAPIKey),
		makeAccWithLoad(2, 1, 10, nil, AccountTypeAPIKey),
		makeAccWithLoad(3, 1, 10, nil, AccountTypeAPIKey),
		makeAccWithLoad(4, 1, 20, nil, AccountTypeAPIKey),
	}
	result := filterByMinLoadRate(accounts)
	require.Len(t, result, 2)
	require.Equal(t, int64(2), result[0].account.ID)
	require.Equal(t, int64(3), result[1].account.ID)
}

// --- selectByLRU ---

func TestSelectByLRU_Empty(t *testing.T) {
	result := selectByLRU(nil, false)
	require.Nil(t, result)
}

func TestSelectByLRU_Single(t *testing.T) {
	accounts := []accountWithLoad{makeAccWithLoad(1, 1, 10, nil, AccountTypeAPIKey)}
	result := selectByLRU(accounts, false)
	require.NotNil(t, result)
	require.Equal(t, int64(1), result.account.ID)
}

func TestSelectByLRU_NilLastUsedAtWins(t *testing.T) {
	now := time.Now()
	accounts := []accountWithLoad{
		makeAccWithLoad(1, 1, 10, testTimePtr(now), AccountTypeAPIKey),
		makeAccWithLoad(2, 1, 10, nil, AccountTypeAPIKey),
		makeAccWithLoad(3, 1, 10, testTimePtr(now.Add(-1*time.Hour)), AccountTypeAPIKey),
	}
	result := selectByLRU(accounts, false)
	require.NotNil(t, result)
	require.Equal(t, int64(2), result.account.ID)
}

func TestSelectByLRU_EarliestTimeWins(t *testing.T) {
	now := time.Now()
	accounts := []accountWithLoad{
		makeAccWithLoad(1, 1, 10, testTimePtr(now), AccountTypeAPIKey),
		makeAccWithLoad(2, 1, 10, testTimePtr(now.Add(-1*time.Hour)), AccountTypeAPIKey),
		makeAccWithLoad(3, 1, 10, testTimePtr(now.Add(-2*time.Hour)), AccountTypeAPIKey),
	}
	result := selectByLRU(accounts, false)
	require.NotNil(t, result)
	require.Equal(t, int64(3), result.account.ID)
}

func TestSelectByLRU_TiePreferOAuth(t *testing.T) {
	now := time.Now()
	// 账号 1/2 LastUsedAt 相同，且同为最小值。
	accounts := []accountWithLoad{
		makeAccWithLoad(1, 1, 10, testTimePtr(now), AccountTypeAPIKey),
		makeAccWithLoad(2, 1, 10, testTimePtr(now), AccountTypeOAuth),
		makeAccWithLoad(3, 1, 10, testTimePtr(now.Add(1*time.Hour)), AccountTypeAPIKey),
	}
	for i := 0; i < 50; i++ {
		result := selectByLRU(accounts, true)
		require.NotNil(t, result)
		require.Equal(t, AccountTypeOAuth, result.account.Type)
		require.Equal(t, int64(2), result.account.ID)
	}
}
