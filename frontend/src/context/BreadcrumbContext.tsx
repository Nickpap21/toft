"use client";

import { createContext, useContext, useState } from "react";

export interface BreadcrumbItem {
  label: string;
  href: string;
}

interface BreadcrumbContextType {
  items: BreadcrumbItem[];
  set: (items: BreadcrumbItem[]) => void;
}

export const BreadcrumbContext = createContext<BreadcrumbContextType>({
  items: [],
  set: () => {},
});

export function BreadcrumbProvider({ children }: { children: React.ReactNode }) {
  const [items, setItems] = useState<BreadcrumbItem[]>([]);
  return (
    <BreadcrumbContext.Provider value={{ items, set: setItems }}>
      {children}
    </BreadcrumbContext.Provider>
  );
}

export const useBreadcrumbs = () => useContext(BreadcrumbContext);
