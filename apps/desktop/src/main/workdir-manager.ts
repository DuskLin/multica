import { execFile } from "node:child_process";
import { parseWorkdirCommand, parseWorkdirResponse, type WorkdirResponse } from "@multica/core/api/workdirs";

const WORKDIR_COMMAND_TIMEOUT_MS = 31 * 60 * 1000;
const WORKDIR_BACKGROUND_REQUEST_TIMEOUT_MS = 30 * 1000;
const WORKDIR_MAX_BUFFER_BYTES = 32 * 1024 * 1024;

/**
 * Run the owner's explicit storage-management command across local profiles.
 * Credentials stay inside the CLI process. Arguments are validated and passed
 * directly to execFile, never through a shell or a renderer-supplied path.
 */
export function runWorkdirCommand(
  binary: string,
  environment: NodeJS.ProcessEnv,
  input: unknown,
): Promise<WorkdirResponse> {
  const command = parseWorkdirCommand(input);
  const backgroundRequest = command.action === "operations" || command.action === "start-delete" || command.action === "start-reclaim";
  const args = ["daemon", "workdirs", "--all-profiles", "--action", command.action];
  if (command.action === "delete" || command.action === "start-delete") args.push("--target-profile", command.profile, "--id", command.id);
  return new Promise((resolve, reject) => {
    execFile(binary, args, {
      timeout: backgroundRequest ? WORKDIR_BACKGROUND_REQUEST_TIMEOUT_MS : WORKDIR_COMMAND_TIMEOUT_MS,
      maxBuffer: WORKDIR_MAX_BUFFER_BYTES,
      env: environment,
    }, (error, stdout, stderr) => {
      if (error) {
        reject(new Error(stderr.trim() || "Local work-directory command failed"));
        return;
      }
      try {
        const data: unknown = JSON.parse(stdout);
        const response = parseWorkdirResponse(data);
        if (command.action === "start-delete" && !response.operations.some((operation) => operation.action === "delete" && operation.profile === command.profile && operation.directoryId === command.id)) {
          throw new Error("Local daemon did not acknowledge background deletion");
        }
        resolve(response);
      } catch (parseError) { reject(parseError); }
    });
  });
}
