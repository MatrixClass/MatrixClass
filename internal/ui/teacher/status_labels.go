package teacher

func transferStatusLabel(status string) string {
	switch status {
	case "completed":
		return "已完成"
	case "running", "transferring":
		return "传输中"
	case "pending":
		return "等待中"
	case "failed":
		return "失败"
	case "cancelled":
		return "已取消"
	case "retrying":
		return "重试中"
	default:
		return status
	}
}

func remoteStatusLabel(status string) string {
	switch status {
	case "completed":
		return "已完成"
	case "running":
		return "执行中"
	case "failed":
		return "失败"
	case "cancelled":
		return "已取消"
	case "rejected":
		return "已拒绝"
	default:
		return status
	}
}
