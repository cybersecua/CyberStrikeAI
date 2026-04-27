package handler

import (
	"testing"

	"cyberstrike-ai/internal/robot"
)

func TestRobotHandler_ImplementsStreamingMessageHandler(t *testing.T) {
	var _ robot.StreamingMessageHandler = (*RobotHandler)(nil)
}
