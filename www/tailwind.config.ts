import type { Config } from "tailwindcss";
import preset from "@flanksource/clicky-ui/tailwind-preset";

const config: Config = {
  presets: [preset as Config],
  content: ["./index.html", "./src/**/*.{ts,tsx}", "./node_modules/@flanksource/clicky-ui/dist/**/*.js"],
};

export default config;
