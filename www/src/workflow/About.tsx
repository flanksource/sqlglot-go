import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Button, Modal } from "@flanksource/clicky-ui/components";
import { Callout } from "@flanksource/clicky-ui/data";
import { postText } from "../api";

export function About() {
  const [open, setOpen] = useState(false);
  const version = useQuery({
    queryKey: ["version"],
    queryFn: async () => {
      const value = await postText("/version", {});
      if (typeof value !== "string")
        throw new Error("Invalid server version response");
      return value;
    },
    enabled: open,
  });
  return (
    <>
      <Button variant="ghost" size="sm" onClick={() => setOpen(true)}>
        About
      </Button>
      <Modal
        open={open}
        onClose={() => setOpen(false)}
        title="About sqlglot"
        size="sm"
      >
        {version.isError ? (
          <Callout variant="caution" title="Version unavailable">
            {version.error.message}
          </Callout>
        ) : (
          <p className="text-sm">
            Server version: <code>{version.data ?? "Loading…"}</code>
          </p>
        )}
      </Modal>
    </>
  );
}
