import { mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import { spawn } from "node:child_process";

const root = resolve(import.meta.dirname, "../..");
const temporary = await mkdtemp(join(tmpdir(), "factory-ui-e2e-"));
const binary = join(temporary, "factory-server");
const database = join(temporary, "factory.sqlite3");

function run(command, args) {
  return new Promise((resolveRun, rejectRun) => {
    const child = spawn(command, args, { cwd: root, stdio: "inherit" });
    child.once("error", rejectRun);
    child.once("exit", (code) => {
      if (code === 0) resolveRun();
      else rejectRun(new Error(`${command} exited with ${code}`));
    });
  });
}

await run("go", ["build", "-o", binary, "./cmd/factory-server"]);
const server = spawn(
  binary,
  ["-listen", "127.0.0.1:17437", "-database", database],
  { cwd: root, stdio: "inherit" },
);

let stopping = false;
async function stop(signal = "SIGTERM") {
  if (stopping) return;
  stopping = true;
  server.kill(signal);
  await new Promise((resolveStop) => server.once("exit", resolveStop));
  await rm(temporary, { recursive: true, force: true });
  process.exit(0);
}

process.on("SIGINT", () => void stop("SIGINT"));
process.on("SIGTERM", () => void stop("SIGTERM"));
server.once("exit", async (code) => {
  if (!stopping) {
    await rm(temporary, { recursive: true, force: true });
    process.exit(code ?? 1);
  }
});
