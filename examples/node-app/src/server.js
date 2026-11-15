import { createServer } from "./app.js";

const port = Number(process.env.PORT ?? 3000);
const server = createServer();
server.listen(port, "0.0.0.0", () => {
  console.log(`listening on http://0.0.0.0:${port}`);
});
