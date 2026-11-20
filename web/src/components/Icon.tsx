export function PrinterIcon({size = 20}: {size?: number}) {
  return (
    <svg fill="none" height={size} viewBox="0 0 24 24" width={size}>
      <path
        d="M7 8V4.5A1.5 1.5 0 0 1 8.5 3h7A1.5 1.5 0 0 1 17 4.5V8M7 17H5.5A1.5 1.5 0 0 1 4 15.5v-6A1.5 1.5 0 0 1 5.5 8h13A1.5 1.5 0 0 1 20 9.5v6a1.5 1.5 0 0 1-1.5 1.5H17M7 13h10v7.5a.5.5 0 0 1-.5.5h-9a.5.5 0 0 1-.5-.5V13Z"
        stroke="currentColor"
        strokeLinecap="round"
        strokeLinejoin="round"
        strokeWidth="1.7"
      />
    </svg>
  );
}

export function ArrowLeftIcon({size = 20}: {size?: number}) {
  return (
    <svg fill="none" height={size} viewBox="0 0 24 24" width={size}>
      <path d="M15 5 8 12l7 7" stroke="currentColor" strokeLinecap="round" strokeLinejoin="round" strokeWidth="1.9" />
    </svg>
  );
}

export function ChevronRightIcon({size = 20}: {size?: number}) {
  return (
    <svg fill="none" height={size} viewBox="0 0 24 24" width={size}>
      <path d="m9 5 7 7-7 7" stroke="currentColor" strokeLinecap="round" strokeLinejoin="round" strokeWidth="1.9" />
    </svg>
  );
}

export function UploadIcon({size = 24}: {size?: number}) {
  return (
    <svg fill="none" height={size} viewBox="0 0 24 24" width={size}>
      <path
        d="M12 16V4m0 0L7.5 8.5M12 4l4.5 4.5M4 15v3a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2v-3"
        stroke="currentColor"
        strokeLinecap="round"
        strokeLinejoin="round"
        strokeWidth="1.7"
      />
    </svg>
  );
}

export function FileIcon({size = 22}: {size?: number}) {
  return (
    <svg fill="none" height={size} viewBox="0 0 24 24" width={size}>
      <path
        d="M14 3H7a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h10a2 2 0 0 0 2-2V8l-5-5Z"
        stroke="currentColor"
        strokeLinejoin="round"
        strokeWidth="1.7"
      />
      <path d="M14 3v5h5" stroke="currentColor" strokeLinejoin="round" strokeWidth="1.7" />
    </svg>
  );
}

export function TrashIcon({size = 18}: {size?: number}) {
  return (
    <svg fill="none" height={size} viewBox="0 0 24 24" width={size}>
      <path
        d="M4 7h16M9 7V5a1 1 0 0 1 1-1h4a1 1 0 0 1 1 1v2m-9 0 1 12a2 2 0 0 0 2 2h6a2 2 0 0 0 2-2l1-12"
        stroke="currentColor"
        strokeLinecap="round"
        strokeLinejoin="round"
        strokeWidth="1.7"
      />
    </svg>
  );
}

export function PlusIcon({size = 18}: {size?: number}) {
  return (
    <svg fill="none" height={size} viewBox="0 0 24 24" width={size}>
      <path d="M12 5v14M5 12h14" stroke="currentColor" strokeLinecap="round" strokeWidth="1.9" />
    </svg>
  );
}

export function MinusIcon({size = 18}: {size?: number}) {
  return (
    <svg fill="none" height={size} viewBox="0 0 24 24" width={size}>
      <path d="M5 12h14" stroke="currentColor" strokeLinecap="round" strokeWidth="1.9" />
    </svg>
  );
}

export function RefreshIcon({size = 18}: {size?: number}) {
  return (
    <svg fill="none" height={size} viewBox="0 0 24 24" width={size}>
      <path
        d="M20 12a8 8 0 1 1-2.6-5.9M20 4v5h-5"
        stroke="currentColor"
        strokeLinecap="round"
        strokeLinejoin="round"
        strokeWidth="1.7"
      />
    </svg>
  );
}

export function LogoutIcon({size = 18}: {size?: number}) {
  return (
    <svg fill="none" height={size} viewBox="0 0 24 24" width={size}>
      <path
        d="M15 5h3a2 2 0 0 1 2 2v10a2 2 0 0 1-2 2h-3M10 16l-4-4 4-4M6 12h9"
        stroke="currentColor"
        strokeLinecap="round"
        strokeLinejoin="round"
        strokeWidth="1.7"
      />
    </svg>
  );
}

export function StopIcon({size = 16}: {size?: number}) {
  return (
    <svg fill="none" height={size} viewBox="0 0 24 24" width={size}>
      <rect height="12" rx="2" stroke="currentColor" strokeWidth="1.8" width="12" x="6" y="6" />
    </svg>
  );
}

export function ListIcon({size = 18}: {size?: number}) {
  return (
    <svg fill="none" height={size} viewBox="0 0 24 24" width={size}>
      <path
        d="M8 6h12M8 12h12M8 18h12M4 6h.01M4 12h.01M4 18h.01"
        stroke="currentColor"
        strokeLinecap="round"
        strokeWidth="1.8"
      />
    </svg>
  );
}

export function CopyIcon({size = 18}: {size?: number}) {
  return (
    <svg fill="none" height={size} viewBox="0 0 24 24" width={size}>
      <rect height="13" rx="2" stroke="currentColor" strokeWidth="1.7" width="13" x="8" y="8" />
      <path
        d="M16 8V5a2 2 0 0 0-2-2H5a2 2 0 0 0-2 2v9a2 2 0 0 0 2 2h3"
        stroke="currentColor"
        strokeLinejoin="round"
        strokeWidth="1.7"
      />
    </svg>
  );
}

export function UserIcon({size = 18}: {size?: number}) {
  return (
    <svg fill="none" height={size} viewBox="0 0 24 24" width={size}>
      <circle cx="12" cy="8" r="4" stroke="currentColor" strokeWidth="1.7" />
      <path
        d="M4.5 20a7.5 7.5 0 0 1 15 0"
        stroke="currentColor"
        strokeLinecap="round"
        strokeWidth="1.7"
      />
    </svg>
  );
}

export function LockIcon({size = 18}: {size?: number}) {
  return (
    <svg fill="none" height={size} viewBox="0 0 24 24" width={size}>
      <rect height="10" rx="2" stroke="currentColor" strokeWidth="1.7" width="16" x="4" y="11" />
      <path d="M8 11V7a4 4 0 0 1 8 0v4" stroke="currentColor" strokeLinecap="round" strokeWidth="1.7" />
    </svg>
  );
}

export function AlertIcon({size = 18}: {size?: number}) {
  return (
    <svg fill="none" height={size} viewBox="0 0 24 24" width={size}>
      <path
        d="M12 4.5 3.5 19h17L12 4.5Z"
        stroke="currentColor"
        strokeLinejoin="round"
        strokeWidth="1.7"
      />
      <path d="M12 10v4M12 17h.01" stroke="currentColor" strokeLinecap="round" strokeWidth="1.9" />
    </svg>
  );
}

export function CloseIcon({size = 16}: {size?: number}) {
  return (
    <svg fill="none" height={size} viewBox="0 0 24 24" width={size}>
      <path d="m6 6 12 12M18 6 6 18" stroke="currentColor" strokeLinecap="round" strokeWidth="1.9" />
    </svg>
  );
}

export function ArrowRightIcon({size = 18}: {size?: number}) {
  return (
    <svg fill="none" height={size} viewBox="0 0 24 24" width={size}>
      <path d="M5 12h14m0 0-6-6m6 6-6 6" stroke="currentColor" strokeLinecap="round" strokeLinejoin="round" strokeWidth="1.8" />
    </svg>
  );
}

export function CheckIcon({size = 16}: {size?: number}) {
  return (
    <svg fill="none" height={size} viewBox="0 0 24 24" width={size}>
      <path d="m5 12.5 4.5 4.5L19 7" stroke="currentColor" strokeLinecap="round" strokeLinejoin="round" strokeWidth="2" />
    </svg>
  );
}
