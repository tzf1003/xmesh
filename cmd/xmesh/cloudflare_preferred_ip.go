package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"io"
	"math/rand/v2"
	"net/netip"
	"os"
	"os/signal"
	"path/filepath"
	"time"

	"github.com/tzf1003/xmesh/internal/cfpreferred"
)

const preferredIPUsage = `用法: xmesh cloudflare preferred-ip [参数]

优选 Cloudflare 边缘 IP：从 Cloudflare 公布的 IP 段随机抽样，对 --host 做 TLS 握手测延时与丢包，按稳定程度排序。
结果写入 --out 指定的列表文件后，在 xmesh.yaml 的 mesh.cloudflare.preferred_ips_file 填这个文件即可，拨号时每次重新读取。
也可以用 --import 导入别人优选好的列表（每行一个 IP，或 CloudflareSpeedTest 的 result.csv），只接受 Cloudflare IP 段内的地址。

  --host 主机名   用来握手的隧道主机名（对端的 xmesh-…域名；测速需要它的 SNI），扫描时必填
  --count N       抽样数量，缺省 200      --tries N  每个 IP 的握手次数，缺省 3
  --top N         保留最好的 N 个，缺省 8    --ipv6     同时抽样 IPv6 段（本机要有 IPv6 出口）
  --import 文件   不扫描，直接校验并导入（- 表示标准输入）
  --out 文件      写入列表文件（绝对路径）；缺省只打印`

func cloudflarePreferredIP(args []string, out io.Writer, in io.Reader) error {
	fs := flag.NewFlagSet("cloudflare preferred-ip", flag.ContinueOnError)
	host := fs.String("host", "", "")
	count := fs.Int("count", 200, "")
	tries := fs.Int("tries", 3, "")
	top := fs.Int("top", 8, "")
	v6 := fs.Bool("ipv6", false, "")
	importPath := fs.String("import", "", "")
	outPath := fs.String("out", "", "")
	fs.SetOutput(io.Discard)
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("%w\n\n%s", err, preferredIPUsage)
	}
	if fs.NArg() > 0 || *top < 1 || *count < 1 || *tries < 1 {
		return fmt.Errorf("参数无效\n\n%s", preferredIPUsage)
	}
	if *outPath != "" && !filepath.IsAbs(*outPath) {
		return fmt.Errorf("--out 须为绝对路径")
	}
	var list []netip.Addr
	if *importPath != "" {
		r := in
		if *importPath != "-" {
			f, err := os.Open(*importPath)
			if err != nil {
				return err
			}
			defer f.Close()
			r = f
		}
		var err error
		if list, err = cfpreferred.ParseList(r); err != nil {
			return err
		}
	} else {
		if *host == "" {
			return fmt.Errorf("扫描需要 --host\n\n%s", preferredIPUsage)
		}
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
		defer stop()
		fmt.Fprintf(out, "抽样 %d 个地址，每个握手 %d 次…\n", *count, *tries)
		rng := rand.New(rand.NewPCG(uint64(time.Now().UnixNano()), 1))
		results := cfpreferred.Scan(ctx, cfpreferred.Sample(*count, *v6, rng), *host, *tries, 16, 3*time.Second)
		if len(results) == 0 {
			return fmt.Errorf("没有任何地址握手成功：检查 --host 是否是对端隧道主机名，以及本机网络")
		}
		for i, r := range results {
			if i >= *top {
				break
			}
			fmt.Fprintf(out, "%-40s 延时 %4d ms  丢包 %3.0f%%\n", r.Addr, r.Latency.Milliseconds(), r.LossRate()*100)
			list = append(list, r.Addr)
		}
	}
	if len(list) == 0 {
		return fmt.Errorf("列表里没有地址")
	}
	if *outPath == "" {
		if *importPath != "" {
			for _, a := range list {
				fmt.Fprintln(out, a)
			}
		}
		return nil
	}
	var buf bytes.Buffer
	buf.WriteString("# xmesh 优选 IP，每行一个 Cloudflare 边缘地址\n")
	for _, a := range list {
		fmt.Fprintln(&buf, a)
	}
	if err := os.WriteFile(*outPath, buf.Bytes(), 0o600); err != nil {
		return err
	}
	fmt.Fprintf(out, "已写入 %d 个地址到 %s；在 mesh.cloudflare.preferred_ips_file 填这个路径\n", len(list), *outPath)
	return nil
}
