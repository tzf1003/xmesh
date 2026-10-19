import http from "node:http";
const server = http.createServer((request, response) => {
  response.setHeader("Content-Type", "text/html; charset=utf-8");
  response.end(
    `<!doctype html><html><head><title>UI test fixture</title></head><body><label>Username<input aria-label="Username"></label><button onclick="document.querySelector('output').textContent='Found '+document.querySelector('input').value">Search</button><output data-testid="results"></output></body></html>`,
  );
});
server.listen(Number(process.env.PORT ?? 3456), "127.0.0.1", () =>
  process.stdout.write(
    `Fixture listening on http://127.0.0.1:${server.address().port}\n`,
  ),
);
