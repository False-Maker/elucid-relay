import React from "react";
import { createRoot } from "react-dom/client";
import { App } from "./App";
import { Providers } from "./app/providers";
import "./styles/globals.css";
import "./styles.css";

createRoot(document.getElementById("root")!).render(
  <React.StrictMode>
    <Providers>
      <App />
    </Providers>
  </React.StrictMode>,
);
