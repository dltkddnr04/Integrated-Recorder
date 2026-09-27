import { Link } from '@tanstack/react-router'
import { FileQuestion } from 'lucide-react'
import { Button } from '@/components/ui/button'
export function NotFoundPage() { return <div className="mx-auto grid min-h-[65vh] max-w-md place-items-center text-center"><div><div className="mx-auto grid h-14 w-14 place-items-center rounded-2xl bg-muted"><FileQuestion className="h-7 w-7 text-muted-foreground" /></div><h1 className="mt-5 text-xl font-semibold">페이지를 찾을 수 없습니다</h1><p className="mt-2 text-sm text-muted-foreground">주소가 잘못되었거나 이동한 페이지입니다.</p><Link to="/"><Button className="mt-5">대시보드로 이동</Button></Link></div></div> }
