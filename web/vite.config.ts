import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

export default defineConfig({
  plugins: [react()],
  // In development the UI runs on its own port and proxies the API, so the
  // Go server does not have to serve the built assets while iterating.
  server: {
    proxy: { "/api": "http://localhost:8080" },
  },
});
