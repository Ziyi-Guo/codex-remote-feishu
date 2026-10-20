package gateway

import (
	"context"

	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"

	"github.com/kxn/codex-remote-feishu/internal/core/agentproto"
	"github.com/kxn/codex-remote-feishu/internal/core/control"
	"github.com/kxn/codex-remote-feishu/internal/limitedgroups"
)

type RoutingEnv struct {
	GatewayID            string
	SurfaceForCardAction func(CardActionSurfaceLookup) string
}

type CardActionSurfaceLookup struct {
	MessageID               string
	PayloadSurfaceSessionID string
	ChatID                  string
	OperatorID              string
}

type QuotedMessageInputs struct {
	Inputs []agentproto.Input
	Files  []control.ActionFileAttachment
}

type InboundEnv struct {
	LimitedGroups                    []limitedgroups.Policy
	AcceptLimitedMessage             func(chatID, userID, messageID string) (bool, error)
	DeliverLimitedInboundFailure     func(context.Context, string, string, string, string, string)
	GatewayID                        string
	BotOpenID                        string
	LookupSurfaceMessage             func(messageID string) string
	ParseTextActionWithoutCatalog    func(text string) (control.Action, bool)
	QuotedInputs                     func(context.Context, *larkim.EventMessage) []agentproto.Input
	QuotedMessageInputs              func(context.Context, *larkim.EventMessage) QuotedMessageInputs
	ParsePostInputs                  func(context.Context, string, string) ([]agentproto.Input, string, error)
	BuildMergeForwardStructuredInput func(context.Context, *larkim.EventMessage) (string, []agentproto.Input, error)
	RecordSurfaceMessage             func(messageID, surfaceSessionID string)
	PrimaryGatewayForChat            func(chatID string) string
	DownloadImage                    func(context.Context, string, string) (string, string, error)
	DownloadFile                     func(context.Context, string, string, string) (string, error)
	DeliverAsyncInboundFailure       func(context.Context, string, string, string, string, string)
}

type ActionDispatcher func(context.Context, control.Action) error
