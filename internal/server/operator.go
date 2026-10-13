package server

import (
	"bufio"
	"context"
	"log/slog"
	"net"
	"time"

	"collab-ai/internal/hub"
	"collab-ai/internal/protocol"
	"collab-ai/internal/store"
)

// OperatorServer accepts one-shot operator requests. Only peers whose kernel-
// reported UID matches the configured operator are served; the request frame of
// any other peer is never read.
type OperatorServer struct {
	*Server
	uid int
}

// NewOperator creates an operator endpoint bound to socketPath (not yet listening).
func NewOperator(socketPath string, h *hub.Hub, operatorUID int, log *slog.Logger) *OperatorServer {
	o := &OperatorServer{Server: &Server{socketPath: socketPath, hub: h, log: log}, uid: operatorUID}
	o.handle = o.handleOperator
	return o
}

func (o *OperatorServer) handleOperator(ctx context.Context, conn net.Conn) {
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()

	uid, err := peerUID(conn)
	if err != nil || uid != o.uid {
		if err != nil {
			uid = -1
			o.log.Warn("operator peer credentials unavailable", "error", err)
		}
		o.audit(ctx, uid, store.AuditForbidden, "peer UID is not the operator")
		o.reply(conn, protocol.Message{Type: protocol.TypeError, Code: protocol.ErrForbidden, Detail: "this endpoint accepts only the operator account"})
		return
	}

	conn.SetReadDeadline(time.Now().Add(helloTimeout))
	r := bufio.NewReader(conn)
	var msg protocol.Message
	if err := protocol.ReadFrame(r, &msg); err != nil {
		o.audit(ctx, uid, store.AuditRejected, "unreadable request frame")
		o.reply(conn, protocol.Message{Type: protocol.TypeError, Code: protocol.ErrMalformedFrame, Detail: "expected one operator request frame"})
		return
	}
	conn.SetReadDeadline(time.Time{})
	if msg.Type == protocol.TypeOperatorSessionOpen {
		o.runSession(ctx, operatorSessionPeer{conn: conn, r: r, uid: uid}, msg)
		return
	}
	o.reply(conn, o.hub.SubmitOperator(ctx, uid, msg))
}

func (o *OperatorServer) audit(ctx context.Context, uid int, result, detail string) {
	if err := o.hub.AuditConnection(ctx, uid, result, detail); err != nil {
		o.log.Error("operator connection audit failed", "error", err)
	}
}

func (o *OperatorServer) reply(conn net.Conn, msg protocol.Message) error {
	conn.SetWriteDeadline(time.Now().Add(writeTimeout))
	err := protocol.WriteFrame(conn, msg)
	if err != nil {
		o.log.Debug("operator write error", "error", err)
	}
	return err
}
