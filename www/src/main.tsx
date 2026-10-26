import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { setFallbackIconProvider } from "@flanksource/clicky-ui/data";
import { FlanksourceIcon } from "@flanksource/clicky-ui/connections";
import { clickyIconProvider } from "@flanksource/clicky-ui/icons";
import "@flanksource/clicky-ui/styles.css";
import "./styles.css";
import App from "./App";

// Connection types are carried as runtime icon names (x-enum-icons); without a
// provider the type picker renders `?` boxes.
setFallbackIconProvider(clickyIconProvider(FlanksourceIcon));

const root = document.getElementById("root");
if (!root) throw new Error("#root element not found");
createRoot(root).render(
  <StrictMode>
    <App />
  </StrictMode>,
);
