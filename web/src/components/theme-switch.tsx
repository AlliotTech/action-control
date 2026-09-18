import { useEffect } from "react";
import { Check, Moon, Sun } from "lucide-react";
import { cn } from "@/lib/utils";
import type { Config } from "@/types";
import { Button } from "@/components/ui/button";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";

type Theme = Config["theme_mode"];

export function ThemeSwitch({
  theme,
  disabled,
  onThemeChange,
}: {
  theme: Theme;
  disabled?: boolean;
  onThemeChange: (theme: Theme) => void;
}) {
  useEffect(() => {
    const dark = document.documentElement.classList.contains("dark");
    document
      .querySelector("meta[name='theme-color']")
      ?.setAttribute("content", dark ? "#020817" : "#fff");
  }, [theme]);

  return (
    <DropdownMenu modal={false}>
      <DropdownMenuTrigger asChild>
        <Button
          variant="ghost"
          size="icon"
          className="relative scale-95 rounded-full"
          disabled={disabled}
        >
          <Sun className="size-[1.2rem] scale-100 rotate-0 transition-all dark:scale-0 dark:-rotate-90" />
          <Moon className="absolute size-[1.2rem] scale-0 rotate-90 transition-all dark:scale-100 dark:rotate-0" />
          <span className="sr-only">切换主题</span>
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end">
        {([
          ["light", "浅色"],
          ["dark", "深色"],
          ["auto", "跟随系统"],
        ] as const).map(([value, label]) => (
          <DropdownMenuItem key={value} onClick={() => onThemeChange(value)}>
            {label}
            <Check className={cn("ms-auto size-3.5", theme !== value && "hidden")} />
          </DropdownMenuItem>
        ))}
      </DropdownMenuContent>
    </DropdownMenu>
  );
}
