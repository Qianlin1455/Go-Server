package service

func CanTransition(from, to string) bool {
	if from == "todo" && to == "doing" {
		return true
	}
	if from == "doing" && (to == "done" || to == "todo") {
		return true
	}
	return false
}
