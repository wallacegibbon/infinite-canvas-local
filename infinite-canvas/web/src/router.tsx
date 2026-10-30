import { createBrowserRouter, Navigate, Outlet } from "react-router-dom";

import UserLayout from "@/layouts/user-layout";
import AssetsPage from "@/pages/assets";
import CanvasProjectPage from "@/pages/canvas/project";
import { CanvasEntry } from "@/pages/canvas/entry";
import ConfigPage from "@/pages/config";
import NotFound from "@/pages/not-found";

export const router = createBrowserRouter([
    {
        element: (
            <UserLayout>
                <Outlet />
            </UserLayout>
        ),
        children: [
            { path: "/", element: <CanvasEntry /> },
            { path: "/assets", element: <AssetsPage /> },
            { path: "/canvas/:id", element: <CanvasProjectPage /> },
            // The list is a panel now, not a page. Old bookmarks land back on a canvas.
            { path: "/canvas", element: <Navigate replace to="/" /> },
            { path: "/config", element: <ConfigPage /> },
        ],
    },
    { path: "*", element: <NotFound /> },
]);
