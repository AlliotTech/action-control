import * as React from "react";
import { errorText } from "./api";
import { Notice } from "./ui";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
  AlertDialogTrigger,
} from "@/components/ui/alert-dialog";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";

export function Modal({
  open,
  onOpenChange,
  title,
  description,
  children,
  className,
  size,
}: {
  open: boolean;
  onOpenChange: (value: boolean) => void;
  title: string;
  description?: string;
  children: React.ReactNode;
  className?: string;
  size?: "default" | "wide" | "media";
}) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className={className} size={size}>
        <DialogHeader>
          <DialogTitle>{title}</DialogTitle>
          <DialogDescription className={description ? undefined : "sr-only"}>
            {description ?? title}
          </DialogDescription>
        </DialogHeader>
        {children}
      </DialogContent>
    </Dialog>
  );
}

export function ConfirmButton({
  children,
  title,
  description,
  onConfirm,
  variant = "outline",
  disabled = false,
}: {
  children: React.ReactNode;
  title: string;
  description: string;
  onConfirm: () => Promise<unknown> | unknown;
  variant?: React.ComponentProps<typeof Button>["variant"];
  disabled?: boolean;
}) {
  const [open, setOpen] = React.useState(false),
    [busy, setBusy] = React.useState(false),
    [error, setError] = React.useState("");
  return (
    <AlertDialog
      open={open}
      onOpenChange={(value) => {
        if (!busy) {
          setOpen(value);
          setError("");
        }
      }}
    >
      <AlertDialogTrigger asChild>
        <Button variant={variant} disabled={disabled}>
          {children}
        </Button>
      </AlertDialogTrigger>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>{title}</AlertDialogTitle>
          <AlertDialogDescription className="whitespace-pre-wrap">
            {description}
          </AlertDialogDescription>
        </AlertDialogHeader>
        {error && <Notice tone="error">{error}</Notice>}
        <AlertDialogFooter>
          <AlertDialogCancel disabled={busy}>取消</AlertDialogCancel>
          <AlertDialogAction
            variant="destructive"
            disabled={busy}
            onClick={async (event) => {
              event.preventDefault();
              setBusy(true);
              setError("");
              try {
                await onConfirm();
                setOpen(false);
              } catch (error) {
                setError(errorText(error));
              } finally {
                setBusy(false);
              }
            }}
          >
            {busy ? "执行中" : "确认执行"}
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );
}
