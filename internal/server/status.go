package server

import (
	"context"
	"fmt"
	"net"
	"time"

	"collab-ai/internal/protocol"
)

// Status uses the existing socket's access boundary and a single response.
// Extra frames on this connection never reach registration or routing.
func (s *Server) writeStatus(ctx context.Context, conn net.Conn) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out, err := s.hub.Status(ctx)
	if err != nil {
		out = protocol.UnavailableStatus()
		out.Reachable = true
		out.Error = fmt.Sprintf("broker status unavailable: %v", err)
	}
	conn.SetWriteDeadline(time.Now().Add(writeTimeout))
	if err := protocol.WriteFrame(conn, protocol.Message{Type: protocol.TypeStatus, Status: &out}); err != nil {
		s.log.Debug("status response failed", "error", err)
	}
}
