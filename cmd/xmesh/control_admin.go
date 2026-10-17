package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"

	v1 "github.com/tzf1003/xmesh/api/control/v1"
	"github.com/tzf1003/xmesh/internal/control"
)

// scopeFlags collects --scope values; each may hold several comma-separated scopes.
type scopeFlags []string

func (s *scopeFlags) String() string { return strings.Join(*s, ",") }
func (s *scopeFlags) Set(v string) error {
	for _, scope := range strings.Split(v, ",") {
		if scope = strings.TrimSpace(scope); scope != "" {
			*s = append(*s, scope)
		}
	}
	return nil
}

// leadingName splits a name given as the first argument ("pats rotate NAME --node x"); the flag
// package would otherwise stop parsing at it.
func leadingName(args []string) (string, []string) {
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		return args[0], args[1:]
	}
	return "", args
}

// single returns the one name the command was given, either as the first argument or as --name.
func single(what, positional, flagged string, rest []string) (string, error) {
	name := positional
	if flagged != "" {
		if name != "" && name != flagged {
			return "", fmt.Errorf("%s 只能指定一个", what)
		}
		name = flagged
	}
	if name == "" {
		return "", fmt.Errorf("缺少%s", what)
	}
	if len(rest) > 0 {
		return "", fmt.Errorf("多余的参数: %s", strings.Join(rest, " "))
	}
	return name, nil
}

// showDone prints v after a reply without a body (204).
func showDone(r *control.Response, err error, v any) error {
	if err != nil {
		return err
	}
	if err = r.Error(); err != nil {
		return err
	}
	out, _ := json.MarshalIndent(v, "", "  ")
	fmt.Println(string(out))
	return nil
}

func controlPair(ctx context.Context, flags *flag.FlagSet, target targetFlags, args []string) error {
	const usage = "用法: xmesh control pair export [--out 文件] | pair import --file 文件 --fingerprint 指纹 [--tags tag:peer,tag:device]"
	if len(args) == 0 || (args[0] != "export" && args[0] != "import") {
		return fmt.Errorf("%s", usage)
	}
	verb := args[0]
	out := flags.String("out", "", "export：把配对文档写到该文件（0600），可交给对端的 pair --import")
	file := flags.String("file", "", "import：对端的配对文件")
	fingerprint := flags.String("fingerprint", "", "import：独立核对过的对端 SHA-256 指纹（冒号与大小写不限）")
	tags := flags.String("tags", "", "import：授予对端的 LAN 角色，逗号分隔；缺省由内核决定（tag:peer,tag:device）")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() > 0 {
		return fmt.Errorf("多余的参数: %s", strings.Join(flags.Args(), " "))
	}
	if verb == "export" {
		if *file != "" || *fingerprint != "" || *tags != "" {
			return fmt.Errorf("--file、--fingerprint、--tags 只用于 import")
		}
		c, err := target.client()
		if err != nil {
			return err
		}
		r, err := c.Do(ctx, "POST", "/v1/pair/export", nil, nil)
		if err != nil {
			return err
		}
		if *out == "" {
			return show(r, nil)
		}
		if err = r.Error(); err != nil {
			return err
		}
		var res v1.PairExportResponse
		if err = json.Unmarshal(r.Body, &res); err != nil {
			return err
		}
		doc, _ := json.MarshalIndent(res.Pair, "", "  ")
		if err = os.WriteFile(*out, doc, 0600); err != nil {
			return err
		}
		return showDone(r, nil, map[string]string{"name": res.Pair.Name, "fingerprint": res.Pair.Fingerprint, "file": *out})
	}
	if *out != "" {
		return fmt.Errorf("--out 只用于 export")
	}
	if *file == "" || *fingerprint == "" {
		return fmt.Errorf("import 需要 --file 和 --fingerprint；指纹必须是你从对端另行拿到的，不能取自配对文件本身\n%s", usage)
	}
	raw, err := os.ReadFile(*file)
	if err != nil {
		return err
	}
	// Both the bare pairing document (xmesh pair --export) and the export reply are accepted.
	var wrapped v1.PairExportResponse
	var doc v1.PairDocument
	if json.Unmarshal(raw, &wrapped) == nil && wrapped.Pair.CertificatePEM != "" {
		doc = wrapped.Pair
	} else if err = json.Unmarshal(raw, &doc); err != nil {
		return fmt.Errorf("配对文件不是有效的 JSON: %w", err)
	}
	if doc.Tags == nil {
		doc.Tags = []string{}
	}
	req := v1.PairImportRequest{Pair: doc, Fingerprint: *fingerprint}
	if *tags != "" {
		req.Tags = strings.Split(*tags, ",")
	}
	c, err := target.client()
	if err != nil {
		return err
	}
	return show(c.Do(ctx, "POST", "/v1/pair/import", req, nil))
}

func controlPats(ctx context.Context, flags *flag.FlagSet, target targetFlags, args []string) error {
	const usage = "用法: xmesh control pats list | create --name 名字 --scope S[,S...] | rotate 名字 | delete 名字"
	if len(args) == 0 {
		return fmt.Errorf("%s", usage)
	}
	verb, rest := args[0], args[1:]
	switch verb {
	case "list":
		if err := flags.Parse(rest); err != nil {
			return err
		}
		c, err := target.client()
		if err != nil {
			return err
		}
		return show(c.Do(ctx, "GET", "/v1/pats", nil, nil))
	case "create":
		var scopes scopeFlags
		name := flags.String("name", "", "PAT 名字（字母、数字、连字符）")
		flags.Var(&scopes, "scope", "授予的 scope（如 devices:read、exec:<设备>、fs:read:<设备>），可重复或用逗号分隔")
		if err := flags.Parse(rest); err != nil {
			return err
		}
		if flags.NArg() > 0 {
			return fmt.Errorf("多余的参数: %s", strings.Join(flags.Args(), " "))
		}
		if *name == "" || len(scopes) == 0 {
			return fmt.Errorf("create 需要 --name 和至少一个 --scope\n%s", usage)
		}
		c, err := target.client()
		if err != nil {
			return err
		}
		return show(c.Do(ctx, "POST", "/v1/pats", v1.PatCreateRequest{Name: *name, Scopes: scopes}, nil))
	case "rotate", "delete":
		positional, rest := leadingName(rest)
		flagged := flags.String("name", "", "PAT 名字")
		if err := flags.Parse(rest); err != nil {
			return err
		}
		name, err := single("PAT 名字", positional, *flagged, flags.Args())
		if err != nil {
			return err
		}
		c, err := target.client()
		if err != nil {
			return err
		}
		path := "/v1/pats/" + url.PathEscape(name)
		if verb == "rotate" {
			return show(c.Do(ctx, "POST", path+"/rotate", nil, nil))
		}
		r, err := c.Do(ctx, "DELETE", path, nil, nil)
		return showDone(r, err, map[string]any{"deleted": true, "name": name})
	}
	return fmt.Errorf("未知的 pats 子命令: %s\n%s", verb, usage)
}

func controlIngress(ctx context.Context, flags *flag.FlagSet, target targetFlags, args []string) error {
	const usage = "用法: xmesh control ingress clients | revoke 客户端ID | password [--password-file 文件]"
	if len(args) == 0 {
		return fmt.Errorf("%s", usage)
	}
	verb, rest := args[0], args[1:]
	switch verb {
	case "clients":
		if err := flags.Parse(rest); err != nil {
			return err
		}
		c, err := target.client()
		if err != nil {
			return err
		}
		return show(c.Do(ctx, "GET", "/v1/ingress/clients", nil, nil))
	case "revoke":
		positional, rest := leadingName(rest)
		flagged := flags.String("client-id", "", "OAuth 客户端 ID")
		if err := flags.Parse(rest); err != nil {
			return err
		}
		id, err := single("客户端 ID", positional, *flagged, flags.Args())
		if err != nil {
			return err
		}
		c, err := target.client()
		if err != nil {
			return err
		}
		r, err := c.Do(ctx, "DELETE", "/v1/ingress/clients/"+url.PathEscape(id), nil, nil)
		return showDone(r, err, map[string]any{"revoked": true, "client_id": id})
	case "password":
		file := flags.String("password-file", "", "新登录口令所在的文件；缺省或 - 从标准输入读（交互终端下不回显）")
		if err := flags.Parse(rest); err != nil {
			return err
		}
		if flags.NArg() > 0 {
			return fmt.Errorf("口令不能放在命令行上，请用 --password-file 或标准输入\n%s", usage)
		}
		password, err := readSecret(*file, controlStdin, os.Stderr)
		if err != nil {
			return err
		}
		c, err := target.client()
		if err != nil {
			return err
		}
		return show(c.Do(ctx, "PUT", "/v1/ingress/password", v1.IngressPasswordRequest{Password: password}, nil))
	}
	return fmt.Errorf("未知的 ingress 子命令: %s\n%s", verb, usage)
}

// controlStdin is where `ingress password` reads from when no file is given.
var controlStdin io.Reader = os.Stdin
