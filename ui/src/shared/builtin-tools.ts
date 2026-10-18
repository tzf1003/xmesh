// The built-in tools the agent role registers (internal/role/agent/specs.go). The names are the
// values `agent.disabled_tools` and `agent.authz[*].tools` use. test/unit/mcp-lib.test.ts checks
// that this list equals the enum of `agent.disabled_tools` items in the config schema.

export interface BuiltinTool {
  name: string;
  zh: string;
  en: string;
}

export const BUILTIN_TOOLS: readonly BuiltinTool[] = [
  { name: "backend.acquire", zh: "获取后端排他租约", en: "Acquire an exclusive backend lease" },
  { name: "backend.renew", zh: "续期后端租约", en: "Renew a backend lease" },
  { name: "backend.release", zh: "释放后端租约", en: "Release a backend lease" },
  { name: "sys.info", zh: "查询设备信息与配额", en: "Query device info and quotas" },
  { name: "fs.read", zh: "读取文件", en: "Read a file" },
  { name: "fs.write", zh: "原子覆盖文件", en: "Atomically overwrite a file" },
  { name: "fs.edit", zh: "精确替换文件文本", en: "Replace exact text in a file" },
  { name: "fs.glob", zh: "按路径模式搜索", en: "Find paths by pattern" },
  { name: "fs.grep", zh: "按 RE2 搜索文本", en: "Search text with RE2" },
  { name: "fs.list", zh: "列出目录", en: "List a directory" },
  { name: "fs.stat", zh: "查询路径元数据", en: "Query path metadata" },
  { name: "fs.sha256", zh: "计算文件哈希", en: "Compute a file's SHA-256" },
  { name: "fs.mkdir", zh: "创建目录", en: "Create a directory" },
  { name: "fs.move", zh: "移动文件或目录", en: "Move a file or directory" },
  { name: "fs.delete", zh: "删除路径", en: "Delete a path" },
  { name: "exec.run", zh: "直接按 argv 执行命令", en: "Run a command directly from argv" },
  { name: "exec.output", zh: "分页读取执行输出", en: "Read command output in pages" },
  { name: "pty.open", zh: "打开交互终端", en: "Open an interactive terminal" },
  { name: "pty.write", zh: "写入所属终端", en: "Write to an owned terminal" },
  { name: "pty.read", zh: "读取终端增量输出", en: "Read incremental terminal output" },
  { name: "pty.resize", zh: "调整终端尺寸", en: "Resize a terminal" },
  { name: "pty.signal", zh: "向终端发送信号", en: "Send a signal to a terminal" },
  { name: "pty.transfer", zh: "交接终端所有权", en: "Hand over terminal ownership" },
  { name: "pty.close", zh: "关闭所属终端", en: "Close an owned terminal" },
  { name: "pty.list", zh: "列出可见终端", en: "List visible terminals" },
];

export const BUILTIN_TOOL_NAMES: readonly string[] = BUILTIN_TOOLS.map((t) => t.name);

export function isBuiltinTool(name: string): boolean {
  return BUILTIN_TOOL_NAMES.includes(name);
}
