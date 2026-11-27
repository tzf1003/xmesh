package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"unicode/utf8"

	v2 "github.com/tzf1003/xmesh/api/control/v2"
	"github.com/tzf1003/xmesh/internal/delegate"
)

const delegateUsage = `用法: xmesh delegate reply --message-id <32位消息ID> [--session-id <会话ID>] (--body <正文> | --body-file <文件> | --stdin) [--node 节点名 | --state-dir 目录]`

var delegateStdin io.Reader = os.Stdin

func delegateCommand(args []string) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		fmt.Println(delegateUsage)
		return nil
	}
	if args[0] != "reply" {
		return fmt.Errorf("未知的 delegate 子命令: %s\n\n%s", args[0], delegateUsage)
	}
	flags := flag.NewFlagSet("delegate reply", flag.ContinueOnError)
	target := addTarget(flags)
	messageID := flags.String("message-id", "", "注入消息携带的 32 位十六进制 id")
	sessionID := flags.String("session-id", "", "可选：活会话 id")
	bodyFlag := flags.String("body", "", "回信正文（UTF-8，不超过 8 KiB）")
	bodyFile := flags.String("body-file", "", "从文件读取回信正文（UTF-8，不超过 8 KiB）")
	fromStdin := flags.Bool("stdin", false, "从标准输入读取回信正文（UTF-8，不超过 8 KiB）")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() > 0 {
		return fmt.Errorf("多余的参数: %s", strings.Join(flags.Args(), " "))
	}
	if *messageID == "" {
		return fmt.Errorf("缺少 --message-id\n%s", delegateUsage)
	}
	sources := 0
	for _, set := range []bool{*bodyFlag != "", *bodyFile != "", *fromStdin} {
		if set {
			sources++
		}
	}
	if sources != 1 {
		return fmt.Errorf("请指定 --body、--body-file 或 --stdin 之一\n%s", delegateUsage)
	}

	var body string
	switch {
	case *bodyFlag != "":
		body = *bodyFlag
	case *bodyFile != "":
		raw, err := readBoundedReply(func() (io.ReadCloser, error) { return os.Open(*bodyFile) })
		if err != nil {
			return err
		}
		body = raw
	case *fromStdin:
		raw, err := readBoundedReader(delegateStdin)
		if err != nil {
			return err
		}
		body = raw
	}

	if body == "" {
		return fmt.Errorf("回信正文不能为空")
	}
	if !utf8.ValidString(body) {
		return fmt.Errorf("回信正文必须是合法 UTF-8")
	}
	if len(body) > delegate.MaxSessionReplyBytes {
		return fmt.Errorf("回信正文超过 %d 字节（8 KiB）上限", delegate.MaxSessionReplyBytes)
	}

	c, err := target.client()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	req := v2.DelegateReplyCreateRequest{
		MessageID: *messageID,
		SessionID: *sessionID,
		Body:      body,
	}
	return show(c.Do(ctx, "POST", "/v2/delegate/replies", req, nil))
}

func readBoundedReply(open func() (io.ReadCloser, error)) (string, error) {
	rc, err := open()
	if err != nil {
		return "", err
	}
	defer rc.Close()
	return readBoundedReader(rc)
}

func readBoundedReader(r io.Reader) (string, error) {
	b, err := io.ReadAll(io.LimitReader(r, delegate.MaxSessionReplyBytes+1))
	if err != nil {
		return "", err
	}
	if len(b) > delegate.MaxSessionReplyBytes {
		return "", fmt.Errorf("回信正文超过 %d 字节（8 KiB）上限", delegate.MaxSessionReplyBytes)
	}
	return string(b), nil
}

const controlDelegateUsage = "用法: xmesh control delegate status\n" +
	"      xmesh control delegate replies list [--state pending|approved|rejected|expired|cancelled]\n" +
	"      xmesh control delegate replies preview <reply_id>\n" +
	"      xmesh control delegate replies approve <reply_id> [--sha256 <64位十六进制>]\n" +
	"      xmesh control delegate replies reject <reply_id>"

func controlDelegate(ctx context.Context, flags *flag.FlagSet, target targetFlags, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("%s", controlDelegateUsage)
	}
	switch args[0] {
	case "status":
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if flags.NArg() > 0 {
			return fmt.Errorf("多余的参数: %s", strings.Join(flags.Args(), " "))
		}
		c, err := target.client()
		if err != nil {
			return err
		}
		return show(c.Do(ctx, "GET", "/v2/delegate", nil, nil))
	case "replies":
		return controlDelegateReplies(ctx, flags, target, args[1:])
	default:
		return fmt.Errorf("未知的 delegate 子命令: %s\n%s", args[0], controlDelegateUsage)
	}
}

func controlDelegateReplies(ctx context.Context, flags *flag.FlagSet, target targetFlags, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("%s", controlDelegateUsage)
	}
	verb, rest := args[0], args[1:]
	switch verb {
	case "list":
		state := flags.String("state", "", "按状态筛选：pending|approved|rejected|expired|cancelled")
		if err := flags.Parse(rest); err != nil {
			return err
		}
		if flags.NArg() > 0 {
			return fmt.Errorf("多余的参数: %s", strings.Join(flags.Args(), " "))
		}
		c, err := target.client()
		if err != nil {
			return err
		}
		path := "/v2/delegate/replies"
		if *state != "" {
			path += "?state=" + queryEscape(*state)
		}
		return show(c.Do(ctx, "GET", path, nil, nil))
	case "preview":
		pos, rest := leadingName(rest)
		idFlag := flags.String("reply-id", "", "待预览的回信草稿 id")
		if err := flags.Parse(rest); err != nil {
			return err
		}
		id, err := single("回信草稿 id", pos, *idFlag, flags.Args())
		if err != nil {
			return err
		}
		c, err := target.client()
		if err != nil {
			return err
		}
		return show(c.Do(ctx, "GET", "/v2/delegate/replies/"+id, nil, nil))
	case "approve":
		pos, rest := leadingName(rest)
		idFlag := flags.String("reply-id", "", "待批准的回信草稿 id")
		sha := flags.String("sha256", "", "可选：核对预览时得到的正文 SHA-256")
		if err := flags.Parse(rest); err != nil {
			return err
		}
		id, err := single("回信草稿 id", pos, *idFlag, flags.Args())
		if err != nil {
			return err
		}
		c, err := target.client()
		if err != nil {
			return err
		}
		return show(c.Do(ctx, "POST", "/v2/delegate/replies/"+id+"/approve", v2.DelegateReplyApproveRequest{BodySHA256: *sha}, nil))
	case "reject":
		pos, rest := leadingName(rest)
		idFlag := flags.String("reply-id", "", "待拒绝的回信草稿 id")
		if err := flags.Parse(rest); err != nil {
			return err
		}
		id, err := single("回信草稿 id", pos, *idFlag, flags.Args())
		if err != nil {
			return err
		}
		c, err := target.client()
		if err != nil {
			return err
		}
		return show(c.Do(ctx, "POST", "/v2/delegate/replies/"+id+"/reject", nil, nil))
	default:
		return fmt.Errorf("未知的 delegate replies 子命令: %s\n%s", verb, controlDelegateUsage)
	}
}
