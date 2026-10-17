import { createContext, useContext, useEffect, useState, type ReactNode } from "react";
import { defaultConfig, loadConfig, type RuntimeConfig } from "./config";

const Ctx = createContext<RuntimeConfig>(defaultConfig);
export const useRuntimeConfig = () => useContext(Ctx);

export function RuntimeConfigProvider({ children }: { children: ReactNode }) {
  const [cfg, setCfg] = useState(defaultConfig);
  useEffect(() => { loadConfig().then(setCfg); }, []);
  return <Ctx.Provider value={cfg}>{children}</Ctx.Provider>;
}
