export class UIError extends Error {
  constructor(
    public code: string,
    message: string,
    public details: Record<string, unknown> = {},
  ) {
    super(message);
    this.name = "UIError";
  }
}
export function failure(error: unknown): {
  code: string;
  message: string;
  details: Record<string, unknown>;
} {
  if (error instanceof UIError)
    return { code: error.code, message: error.message, details: error.details };
  const message = error instanceof Error ? error.message : String(error);
  const code = /strict mode violation/.test(message)
    ? "ambiguous_target"
    : /expect\(|Expected:|AssertionError/.test(message)
      ? "assertion_failed"
      : /Timeout|timeout/i.test(message)
        ? "timeout"
        : /closed|exited|disconnected/i.test(message)
          ? "application_exited"
          : "execution_failed";
  return { code, message: message.slice(0, 4096), details: {} };
}
export function requireValue(
  condition: unknown,
  code: string,
  message: string,
): asserts condition {
  if (!condition) throw new UIError(code, message);
}
