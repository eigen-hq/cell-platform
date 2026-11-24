import { defineConfig } from "vite";
import { svelte } from "@sveltejs/vite-plugin-svelte";

// The console is looked at on a cell (console.<domain>, served by the API):
// its login only returns there.
export default defineConfig({
  plugins: [svelte()],
});
