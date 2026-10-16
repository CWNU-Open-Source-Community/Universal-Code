function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

export function isBuiltinComputerUseServer(value: unknown): boolean {
  return isRecord(value) && value.transport === "stdio" &&
    value.target === "builtin:windows-computer-use" && value.native_source === undefined &&
    isRecord(value.source) && value.source.kind === "builtin" &&
    value.source.uri === "universal-code:computer-use" && value.source.version === "1";
}

export function validBuiltinComputerUseConnection(value: Record<string, unknown>): boolean {
  return isBuiltinComputerUseServer(value) &&
    (value.credential_ref === undefined || value.credential_ref === "") &&
    (value.cwd === undefined || value.cwd === "") &&
    (value.arguments === undefined || Array.isArray(value.arguments) && value.arguments.length === 0) &&
    [value.env, value.env_credential_refs, value.headers].every((map) =>
      map === undefined || isRecord(map) && Object.keys(map).length === 0);
}
