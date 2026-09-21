package service

// Site-managed accounts default to one upstream attempt. An administrator can
// explicitly opt back into retries using the existing account pool settings.
func siteAccountRetriesDisabled(account *Account) bool {
	if !account.IsSiteManaged() {
		return false
	}
	value, configured := account.Credentials["pool_mode_retry_count"]
	return !configured || parsePoolModeRetryCount(value) <= 0
}

func geminiUpstreamMaxAttempts(account *Account) int {
	if siteAccountRetriesDisabled(account) {
		return 1
	}
	return geminiMaxRetries
}
