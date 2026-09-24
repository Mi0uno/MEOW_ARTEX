"use client";

import * as React from "react";

import Link from "next/link";
import { usePathname, useSearchParams } from "next/navigation";

import { ChevronRight } from "lucide-react";

import { Collapsible, CollapsibleContent, CollapsibleTrigger } from "@/components/ui/collapsible";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuGroup,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import {
  SidebarGroup,
  SidebarGroupContent,
  SidebarGroupLabel,
  SidebarMenu,
  SidebarMenuButton,
  SidebarMenuItem,
  SidebarMenuSub,
  SidebarMenuSubButton,
  SidebarMenuSubItem,
  useSidebar,
} from "@/components/ui/sidebar";
import { api } from "@/lib/api";
import { cn } from "@/lib/utils";
import type {
  NavBadge,
  NavGroup,
  NavMainItem,
  NavMainLinkItem,
  NavMainParentItem,
} from "@/navigation/sidebar/sidebar-items";

interface NavMainProps {
  readonly items: readonly NavGroup[];
}
interface NavItemProps {
  readonly item: NavMainItem;
  readonly isItemActive: (item: NavMainItem) => boolean;
  readonly isSubItemActive: (url: string) => boolean;
  readonly isSubmenuOpen: (item: NavMainParentItem) => boolean;
}

interface NavLinkItemProps {
  readonly item: NavMainLinkItem;
  readonly isActive: boolean;
  readonly showIconFallback: boolean;
}

interface NavLinkIconProps {
  readonly item: NavMainLinkItem;
  readonly showFallback: boolean;
}

interface NavDropdownItemProps {
  readonly item: NavMainParentItem;
  readonly isActive: boolean;
  readonly isSubItemActive: (url: string) => boolean;
}

interface NavCollapsibleItemProps {
  readonly item: NavMainParentItem;
  readonly isActive: boolean;
  readonly defaultOpen: boolean;
  readonly isSubItemActive: (url: string) => boolean;
}

function CollapsedIconFallback({ title }: { title: string }) {
  return (
    <span className="flex size-4 shrink-0 items-center justify-center rounded-xs font-medium text-[10px] outline">
      {title.slice(0, 1)}
    </span>
  );
}

function hasSubItems(item: NavMainItem): item is NavMainParentItem {
  return Boolean(item.subItems?.length);
}

export function NavMain({ items }: NavMainProps) {
  // useSearchParams 必须挂在 Suspense 边界内(静态导出预渲染约束),
  // 与任务页 ToolsPage/ToolsPageInner 的拆分同一模式。
  return (
    <React.Suspense fallback={null}>
      <NavMainInner items={items} />
    </React.Suspense>
  );
}

// 把导航 url 拆成路径与 query 两段。同一页面会挂在多个分组(任务中心的"任务/对话"
// 与三阶段分组各自的"任务/对话"),靠 query(?stage=)区分归属。
function splitNavUrl(url: string): { path: string; query: string } {
  const idx = url.indexOf("?");
  return idx === -1 ? { path: url, query: "" } : { path: url.slice(0, idx), query: url.slice(idx + 1) };
}

function NavMainInner({ items }: NavMainProps) {
  const rawPath = usePathname();
  const searchParams = useSearchParams();
  // 路由段含中文，pathname 可能是百分号编码，解码后再与导航 url 比较，保证高亮命中。
  const path = (() => {
    try {
      return decodeURIComponent(rawPath);
    } catch {
      return rawPath;
    }
  })();

  // 当前路由命中的导航 url:优先在「路径相同」的候选里选——query 与当前完全一致者
  // 优先,否则取无 query 的规范链接(任务中心的"任务/对话")。没有精确命中时回退到
  // 「无 query 且路径为当前路径前缀」的最长候选,让详情页(/function/tasks/detail 等)
  // 依然点亮所属分组。这样 /function/tasks?stage=recon 只点亮信息搜集分组的"任务",
  // 不再与任务中心串高亮。
  const activeUrl = React.useMemo(() => {
    const all: string[] = [];
    for (const group of items) {
      for (const item of group.items) {
        if (hasSubItems(item)) {
          for (const sub of item.subItems) all.push(sub.url);
        } else {
          all.push(item.url);
        }
      }
    }
    const pickFrom = (cands: string[]): string => {
      const withQuery = cands.find((u) => {
        const q = splitNavUrl(u).query;
        if (!q) return false;
        return [...new URLSearchParams(q).entries()].every(([k, v]) => searchParams.get(k) === v);
      });
      if (withQuery) return withQuery;
      return cands.find((u) => !splitNavUrl(u).query) ?? cands[0] ?? "";
    };
    const exactPath = all.filter((u) => splitNavUrl(u).path === path);
    if (exactPath.length > 0) return pickFrom(exactPath);
    const prefixCands = all.filter((u) => {
      const { path: p, query } = splitNavUrl(u);
      return !query && p !== path && path.startsWith(`${p}/`);
    });
    if (prefixCands.length > 0) {
      // 最长前缀优先(/system/intercept/approvals 命中 /system/intercept/approvals 而非更短的父路径)。
      return prefixCands.reduce((best, u) => (splitNavUrl(u).path.length > splitNavUrl(best).path.length ? u : best));
    }
    return "";
  }, [items, path, searchParams]);

  const isItemActive = (item: NavMainItem) => {
    if (hasSubItems(item)) {
      return item.subItems.some((sub) => sub.url === activeUrl);
    }

    return item.url === activeUrl;
  };

  const isSubItemActive = (url: string) => {
    return url === activeUrl;
  };

  const isSubmenuOpen = (item: NavMainParentItem) => {
    return item.subItems.some((sub) => sub.url === activeUrl);
  };

  return (
    <>
      {items.map((group) => (
        <SidebarGroup key={group.id}>
          {group.label && (
            <SidebarGroupLabel className="group-data-[collapsible=icon]:pointer-events-none">
              {group.label}
            </SidebarGroupLabel>
          )}
          <SidebarGroupContent>
            <SidebarMenu>
              {group.items.map((item) => (
                <NavItem
                  key={item.id}
                  item={item}
                  isItemActive={isItemActive}
                  isSubItemActive={isSubItemActive}
                  isSubmenuOpen={isSubmenuOpen}
                />
              ))}
            </SidebarMenu>
          </SidebarGroupContent>
        </SidebarGroup>
      ))}
    </>
  );
}

function NavItem({ item, isItemActive, isSubItemActive, isSubmenuOpen }: NavItemProps) {
  const { state, isMobile } = useSidebar();
  const isCollapsedDesktop = state === "collapsed" && !isMobile;

  if (!hasSubItems(item)) {
    return <NavLinkItem item={item} isActive={isItemActive(item)} showIconFallback={isCollapsedDesktop} />;
  }

  if (isCollapsedDesktop) {
    return <NavDropdownItem item={item} isActive={isItemActive(item)} isSubItemActive={isSubItemActive} />;
  }

  return (
    <NavCollapsibleItem
      item={item}
      isActive={isItemActive(item)}
      defaultOpen={isSubmenuOpen(item)}
      isSubItemActive={isSubItemActive}
    />
  );
}

// InterceptPendingBadge polls /api/intercept/pending every 5 s and shows a red
// count badge when there are unresolved approval requests. 行内元素（ml-auto
// 靠右），放在 Link 内容末尾——不用 SidebarMenuBadge 的绝对定位，避免与
// 折叠箭头/右缘内容重叠。
function InterceptPendingBadge() {
  const [count, setCount] = React.useState(0);
  React.useEffect(() => {
    let live = true;
    async function poll() {
      try {
        const list = await api.interceptPending();
        if (live) setCount(list.length);
      } catch {
        /* ignore */
      }
    }
    void poll();
    const t = setInterval(poll, 5000);
    return () => {
      live = false;
      clearInterval(t);
    };
  }, []);
  if (count === 0) return null;
  return (
    <span className="ml-auto flex h-4.5 min-w-[18px] shrink-0 items-center justify-center rounded-full bg-amber-500 px-1 font-semibold text-[10px] text-white leading-none shadow-sm ring-1 ring-amber-400/40">
      {count > 99 ? "99+" : count}
    </span>
  );
}

function NavLinkItem({ item, isActive, showIconFallback }: NavLinkItemProps) {
  return (
    <SidebarMenuItem>
      <SidebarMenuButton asChild aria-disabled={item.disabled} tooltip={item.title} isActive={isActive}>
        <Link
          prefetch={false}
          href={item.url}
          target={item.newTab ? "_blank" : undefined}
          rel={item.newTab ? "noreferrer" : undefined}
        >
          <NavLinkIcon item={item} showFallback={showIconFallback} />
          <span className="min-w-0 truncate">{item.title}</span>
          {item.id === "approvals" ? <InterceptPendingBadge /> : <NavItemBadge badge={item.badge} />}
        </Link>
      </SidebarMenuButton>
    </SidebarMenuItem>
  );
}

function NavLinkIcon({ item, showFallback }: NavLinkIconProps) {
  const Icon = item.icon;

  if (Icon) {
    return <Icon />;
  }

  if (showFallback) {
    return <CollapsedIconFallback title={item.title} />;
  }

  return null;
}

function NavDropdownItem({ item, isActive, isSubItemActive }: NavDropdownItemProps) {
  const Icon = item.icon;

  return (
    <SidebarMenuItem>
      <DropdownMenu>
        <DropdownMenuTrigger asChild>
          <SidebarMenuButton tooltip={item.title} isActive={isActive} disabled={item.disabled}>
            {Icon ? <Icon /> : <CollapsedIconFallback title={item.title} />}
            <span>{item.title}</span>
          </SidebarMenuButton>
        </DropdownMenuTrigger>

        <DropdownMenuContent side="right" align="start" sideOffset={12} className="w-48">
          <DropdownMenuGroup>
            {item.subItems.map((subItem) => {
              const SubIcon = subItem.icon;

              return (
                <DropdownMenuItem key={subItem.id} asChild disabled={subItem.disabled}>
                  <Link
                    prefetch={false}
                    href={subItem.url}
                    target={subItem.newTab ? "_blank" : undefined}
                    rel={subItem.newTab ? "noreferrer" : undefined}
                    aria-current={isSubItemActive(subItem.url) ? "page" : undefined}
                    className="flex items-center gap-2"
                  >
                    {SubIcon && <SubIcon />}
                    <span>{subItem.title}</span>
                  </Link>
                </DropdownMenuItem>
              );
            })}
          </DropdownMenuGroup>
        </DropdownMenuContent>
      </DropdownMenu>
    </SidebarMenuItem>
  );
}

function NavCollapsibleItem({ item, isActive, defaultOpen, isSubItemActive }: NavCollapsibleItemProps) {
  const Icon = item.icon;

  // 展开/收回状态按分类 id 持久化到 localStorage：刷新后保持用户上次的折叠
  // 习惯。挂载后再读存储覆盖初始值（初始值来自当前路由是否命中），避免 SSR
  // 与客户端首帧不一致导致 hydration 警告。
  const storageKey = `sidebar_open_${item.id}`;
  const [open, setOpen] = React.useState(defaultOpen);
  React.useEffect(() => {
    try {
      const stored = window.localStorage.getItem(storageKey);
      if (stored !== null) setOpen(stored === "1");
    } catch {
      /* 隐私模式等场景 localStorage 不可用时静默降级为会话内记忆 */
    }
  }, [storageKey]);
  const handleOpenChange = (value: boolean) => {
    setOpen(value);
    try {
      window.localStorage.setItem(storageKey, value ? "1" : "0");
    } catch {
      /* ignore */
    }
  };

  return (
    <Collapsible asChild open={open} onOpenChange={handleOpenChange} className="group/collapsible">
      <SidebarMenuItem>
        <CollapsibleTrigger asChild>
          <SidebarMenuButton tooltip={item.title} isActive={isActive} disabled={item.disabled}>
            {Icon && <Icon />}
            <span className="min-w-0 truncate">{item.title}</span>
            {/* 徽标与箭头同组靠右：徽标行内化后不再与 ml-auto 的箭头重叠 */}
            <span className="ml-auto flex shrink-0 items-center gap-1">
              <NavItemBadge badge={item.badge} />
              <ChevronRight className="size-4 transition-transform duration-200 group-data-[state=open]/collapsible:rotate-90" />
            </span>
          </SidebarMenuButton>
        </CollapsibleTrigger>

        <CollapsibleContent>
          <SidebarMenuSub>
            {item.subItems.map((subItem) => {
              const SubIcon = subItem.icon;

              return (
                <SidebarMenuSubItem key={subItem.id}>
                  <SidebarMenuSubButton
                    asChild
                    aria-disabled={subItem.disabled}
                    isActive={isSubItemActive(subItem.url)}
                  >
                    <Link
                      prefetch={false}
                      href={subItem.url}
                      target={subItem.newTab ? "_blank" : undefined}
                      rel={subItem.newTab ? "noreferrer" : undefined}
                    >
                      {SubIcon && <SubIcon />}
                      <span className="min-w-0 truncate">{subItem.title}</span>
                      {subItem.id === "approvals" && <InterceptPendingBadge />}
                    </Link>
                  </SidebarMenuSubButton>
                </SidebarMenuSubItem>
              );
            })}
          </SidebarMenuSub>
        </CollapsibleContent>
      </SidebarMenuItem>
    </Collapsible>
  );
}

// NavItemBadge 行内徽标：由父级 flex 排版（ml-auto 由外层控制），不再使用
// SidebarMenuBadge 的绝对定位——那会与折叠箭头/右缘内容重叠。
// wip 渲染为中文「建设中」(占位功能的标识)，new/soon 原样展示。
function NavItemBadge({ badge }: { badge?: NavBadge }) {
  if (!badge) {
    return null;
  }

  return (
    <span
      className={cn(
        "flex h-4.5 shrink-0 items-center justify-center rounded-sm border px-1 font-medium text-[10px] capitalize leading-none",
        (badge === "soon" || badge === "wip") && "border-muted-foreground text-muted-foreground",
        badge === "new" && "border-green-600 text-green-600",
      )}
    >
      {badge === "wip" ? "建设中" : badge}
    </span>
  );
}
