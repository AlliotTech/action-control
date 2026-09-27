import * as React from "react";
import { AlertCircle, CheckCircle2, Info } from "lucide-react";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { cn } from "@/lib/utils";
import { Badge } from "@/components/ui/badge";
import {
  Card as ShadcnCard,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import {
  Empty,
  EmptyContent,
  EmptyDescription,
  EmptyHeader,
  EmptyTitle,
} from "@/components/ui/empty";
import { Spinner as ShadcnSpinner } from "@/components/ui/spinner";

export function Card({
  title,
  description,
  className,
  children,
}: {
  title?: React.ReactNode;
  description?: React.ReactNode;
  className?: string;
  children: React.ReactNode;
}) {
  return (
    <ShadcnCard className={cn("shadow-sm", className)}>
      {(title || description) && (
        <CardHeader>
          {title && <CardTitle>{title}</CardTitle>}
          {description && <CardDescription>{description}</CardDescription>}
        </CardHeader>
      )}
      <CardContent>{children}</CardContent>
    </ShadcnCard>
  );
}

export function PageHeader({
  title,
  description,
  actions,
}: {
  title: string;
  description?: string;
  actions?: React.ReactNode;
}) {
  return (
    <header className="mb-6 flex flex-wrap items-start justify-between gap-4 lg:mb-8">
      <div>
        <h1 className="text-2xl font-bold tracking-tight sm:text-3xl">
          {title}
        </h1>
        {description && (
          <p className="mt-1 text-sm text-muted-foreground sm:text-base">
            {description}
          </p>
        )}
      </div>
      {actions && (
        <div className="flex flex-wrap items-center gap-2">{actions}</div>
      )}
    </header>
  );
}

export function Notice({
  children,
  tone = "info",
}: {
  children: React.ReactNode;
  tone?: "error" | "warning" | "success" | "info";
}) {
  const Icon =
    tone === "error" || tone === "warning"
      ? AlertCircle
      : tone === "success"
        ? CheckCircle2
        : Info;
  return (
    <Alert
      role={tone === "error" ? "alert" : "status"}
      variant={tone === "error" ? "destructive" : "default"}
      className={cn(
        tone === "warning" &&
          "border-amber-500/25 bg-amber-500/10 text-amber-800 dark:text-amber-300",
        tone === "success" &&
          "border-emerald-500/25 bg-emerald-500/10 text-emerald-700 dark:text-emerald-300",
      )}
    >
      <Icon />
      <AlertDescription className="min-w-0 break-words text-current">
        {children}
      </AlertDescription>
    </Alert>
  );
}

export function EmptyState({
  title,
  description,
  action,
}: {
  title: string;
  description?: React.ReactNode;
  action?: React.ReactNode;
}) {
  return (
    <Empty>
      <EmptyHeader>
        <EmptyTitle>{title}</EmptyTitle>
        {description && <EmptyDescription>{description}</EmptyDescription>}
      </EmptyHeader>
      {action && <EmptyContent>{action}</EmptyContent>}
    </Empty>
  );
}

export function Spinner() {
  return (
    <span
      role="status"
      className="inline-flex items-center gap-2 text-sm text-muted-foreground"
    >
      <ShadcnSpinner />
      <span>正在读取…</span>
    </span>
  );
}

export function formatBytes(n: number | null | undefined): string {
  if (n == null || !Number.isFinite(n)) return "—";
  const units = ["B", "KB", "MB", "GB", "TB"];
  let value = n,
    index = 0;
  while (value >= 1024 && index < units.length - 1) {
    value /= 1024;
    index++;
  }
  return `${value.toLocaleString("zh-CN", { maximumFractionDigits: index ? 1 : 0 })} ${units[index]}`;
}

export function formatDate(seconds: number): string {
  return new Date(seconds * 1000).toLocaleString("zh-CN", { hour12: false });
}
