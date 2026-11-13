package compress

const (
	maxGenericLine = 500
	maxGenericHead = 120
	maxGenericTail = 40
)

func compressGeneric(s string) string {
	lines := splitLines(s)
	for i, line := range lines {
		lines[i] = truncateLine(line, maxGenericLine)
	}
	return joinLines(capLines(lines, maxGenericHead, maxGenericTail))
}
