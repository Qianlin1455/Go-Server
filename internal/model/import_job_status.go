package model

// CanTransitionImportJob 判断 ImportJob 状态转换是否合法。
// 这里只校验状态转换方向，不涉及数据库更新、重试次数和租约校验。
func CanTransitionImportJob(from, to string) bool {
	if from == to {
		return false
	}

	switch from {
	case ImportJobQueued:
		return to == ImportJobRunning

	case ImportJobRunning:
		return to == ImportJobSucceeded ||
			to == ImportJobFailed ||
			to == ImportJobQueued

	case ImportJobSucceeded, ImportJobFailed:
		return false

	default:
		return false
	}
}
