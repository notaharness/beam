declare namespace Cloudflare {
  interface Env {
    DB: D1Database;
    LIMITER: RateLimit;
  }
  interface GlobalProps {
    mainModule: typeof import("../src/index");
  }
}
