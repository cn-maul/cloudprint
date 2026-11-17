import {toast} from "@heroui/react";

export function notify(title: string, description?: string) {
  toast(title, {description, variant: "default"});
}

export function notifySuccess(title: string, description?: string) {
  toast(title, {description, variant: "success"});
}

export function notifyError(title: string, description?: string) {
  toast(title, {description, variant: "danger"});
}

export function notifyWarning(title: string, description?: string) {
  toast(title, {description, variant: "warning"});
}
