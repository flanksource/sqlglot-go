import { createContext, useContext, useState, type ReactNode } from "react";

type Drafts = {
  values: Record<string, string>;
  set: (key: string, value: string) => void;
};
const Context = createContext<Drafts | null>(null);

export function DraftProvider({ children }: { children: ReactNode }) {
  const [values, setValues] = useState<Record<string, string>>({});
  return (
    <Context.Provider
      value={{
        values,
        set: (key, value) =>
          setValues((current) => ({ ...current, [key]: value })),
      }}
    >
      {children}
    </Context.Provider>
  );
}

export function useDraft(
  key: string,
  initial = "",
): [string, (value: string) => void] {
  const context = useContext(Context);
  if (!context) throw new Error("DraftProvider is required");
  return [context.values[key] ?? initial, (value) => context.set(key, value)];
}
